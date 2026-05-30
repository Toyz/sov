// Package batch ships the cascading-batch endpoint at /rpc/_batch.
// Owns the route via RouteHandler; framework holds zero batch state.
//
// Batch semantics:
//
//   - Caller POSTs {"calls": {"alias1": {...}, "alias2": {...}}}.
//   - Plugin resolves each entry's destination once and groups
//     entries by endpoint.
//   - Local-only and single-entry remote groups dispatch entry-by-entry
//     through the gateway's full middleware chain (auth+authz+plugin
//     hooks) via gw.Handle.
//   - Remote groups with 2+ entries POST one nested /rpc/_batch to the
//     destination pod (cascading batch). On 404 the plugin falls back
//     to per-entry dispatch and caches the negative answer for 60s.
//
// Caller-facing response shape is unchanged from the original
// framework-owned handler.
//
//	gw.Use(batch.New())
package batch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/rpc"
)

// defaultUnsupportedTTL is how long the plugin remembers that a remote
// address returned 404 on /rpc/_batch.
const defaultUnsupportedTTL = 60 * time.Second

// Config configures the batch plugin. UnsupportedTTL is the cache
// window for "pod doesn't support /rpc/_batch" answers; zero falls
// back to 60s.
type Config struct {
	UnsupportedTTL time.Duration
}

// Plugin is the batch route owner returned by New.
type Plugin struct {
	gw             *gateway.Gateway
	unsupportedTTL time.Duration
	muSupp         sync.RWMutex
	missing        map[string]time.Time
}

// Compile-time proof of the hooks this plugin binds — a signature
// drift here is a build error, not a silent non-binding at runtime.
var (
	_ gateway.Plugin        = (*Plugin)(nil)
	_ gateway.PluginDoc     = (*Plugin)(nil)
	_ gateway.ConfigApplier = (*Plugin)(nil)
	_ gateway.RouteHandler  = (*Plugin)(nil)
)

// New returns the batch plugin from cfg.
func New(cfg Config) *Plugin {
	ttl := cfg.UnsupportedTTL
	if ttl <= 0 {
		ttl = defaultUnsupportedTTL
	}
	return &Plugin{missing: map[string]time.Time{}, unsupportedTTL: ttl}
}

// PluginName surfaces in /rpc/_introspect.plugins[].
func (p *Plugin) PluginName() string { return "batch" }

// Doc surfaces a one-line description in /rpc/_introspect + the explorer.
func (p *Plugin) Doc() string {
	return "Cascading /rpc/_batch — groups calls by destination and coalesces same-pod calls into one round trip."
}

// Apply grabs the gateway pointer for later use.
func (p *Plugin) Apply(g *gateway.Gateway) error { p.gw = g; return nil }

// RoutePatterns claims the batch endpoint.
func (p *Plugin) RoutePatterns() []string { return []string{"/rpc/_batch"} }

// ServeRoute dispatches a batch request.
func (p *Plugin) ServeRoute(ctx context.Context, req *gateway.Request) *gateway.Response {
	if req.Method != http.MethodPost {
		return gateway.ErrorResponse(&rpc.Error{Status: 405, Code: "BAD_REQUEST", Message: "method not allowed"})
	}
	var br gateway.BatchRequest
	if err := json.Unmarshal(req.Body, &br); err != nil {
		return gateway.ErrorResponse(rpc.BadRequest("invalid body: %v", err))
	}
	if len(br.Calls) == 0 {
		return gateway.ErrorResponse(rpc.BadRequest("calls is empty"))
	}

	groups, results := p.groupBatch(ctx, br)

	if len(groups) > 0 {
		var mu sync.Mutex
		var wg sync.WaitGroup
		wg.Add(len(groups))
		for _, grp := range groups {
			go func(grp *batchGroup) {
				defer wg.Done()
				partial := p.dispatchGroup(ctx, req, grp)
				mu.Lock()
				for alias, body := range partial {
					results[alias] = body
				}
				mu.Unlock()
			}(grp)
		}
		wg.Wait()
	}

	body, _ := json.Marshal(gateway.BatchResponse{Results: results})
	return &gateway.Response{Status: 200, Body: body}
}

// batchGroup is a bucket of batch entries that share a destination.
type batchGroup struct {
	isLocal bool
	addr    string
	service string
	calls   map[string]gateway.BatchCall
}

func (p *Plugin) groupBatch(ctx context.Context, br gateway.BatchRequest) ([]*batchGroup, map[string]json.RawMessage) {
	results := map[string]json.RawMessage{}
	local := &batchGroup{isLocal: true, calls: map[string]gateway.BatchCall{}}
	remoteByAddr := map[string]*batchGroup{}

	for alias, call := range br.Calls {
		ep, ok := p.gw.Resolver().Resolve(ctx, call.Service)
		if !ok {
			results[alias] = rpc.MarshalError(rpc.NotFound("service %q not registered", call.Service))
			continue
		}
		if ep.Local {
			local.calls[alias] = call
			continue
		}
		grp, ok := remoteByAddr[ep.RemoteAddr]
		if !ok {
			grp = &batchGroup{addr: ep.RemoteAddr, service: call.Service, calls: map[string]gateway.BatchCall{}}
			remoteByAddr[ep.RemoteAddr] = grp
		}
		grp.calls[alias] = call
	}

	groups := make([]*batchGroup, 0, 1+len(remoteByAddr))
	if len(local.calls) > 0 {
		groups = append(groups, local)
	}
	for _, grp := range remoteByAddr {
		groups = append(groups, grp)
	}
	return groups, results
}

func (p *Plugin) dispatchGroup(ctx context.Context, parent *gateway.Request, grp *batchGroup) map[string]json.RawMessage {
	if grp.isLocal || len(grp.calls) == 1 {
		return p.dispatchPerEntry(ctx, parent, grp.calls)
	}
	if p.unsupported(grp.addr) {
		return p.dispatchPerEntry(ctx, parent, grp.calls)
	}
	results, fellBack := p.dispatchRemoteBatch(ctx, parent, grp.addr, grp.calls)
	if fellBack {
		return p.dispatchPerEntry(ctx, parent, grp.calls)
	}
	return results
}

func (p *Plugin) dispatchPerEntry(ctx context.Context, parent *gateway.Request, calls map[string]gateway.BatchCall) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(calls))
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(len(calls))
	for alias, call := range calls {
		go func(alias string, call gateway.BatchCall) {
			defer wg.Done()
			r := p.runOne(ctx, parent, call)
			mu.Lock()
			out[alias] = r
			mu.Unlock()
		}(alias, call)
	}
	wg.Wait()
	return out
}

func (p *Plugin) runOne(ctx context.Context, parent *gateway.Request, call gateway.BatchCall) json.RawMessage {
	bodyArgs := call.Args
	if len(bodyArgs) == 0 {
		bodyArgs = json.RawMessage(`[]`)
	}
	wrapped, _ := json.Marshal(struct {
		Args json.RawMessage `json:"args"`
	}{Args: bodyArgs})

	sub := &gateway.Request{
		Method: http.MethodPost,
		Path:   "/rpc/" + call.Service + "/" + call.Method,
		// Clone: batch entries dispatch concurrently and the dispatch
		// path mutates req.Header (requestid stamps an id), so each
		// sub-request needs its own map or parallel handlers race on
		// the shared parent map (fatal concurrent map access).
		Header:   parent.Header.Clone(),
		Body:     wrapped,
		RemoteIP: parent.RemoteIP,
		User:     parent.User,
	}
	resp := p.gw.Handle(ctx, sub)
	if resp == nil {
		return rpc.MarshalError(&rpc.Error{Status: 500, Code: "INTERNAL", Message: "nil batch response"})
	}
	return resp.Body
}

func (p *Plugin) dispatchRemoteBatch(ctx context.Context, parent *gateway.Request, addr string, calls map[string]gateway.BatchCall) (map[string]json.RawMessage, bool) {
	nested, _ := json.Marshal(gateway.BatchRequest{Calls: calls})
	hreq, err := p.gw.BuildProxyRequest(ctx, http.MethodPost, addr, "/rpc/_batch", nested, parent)
	if err != nil {
		return failAll(calls, rpc.Internal("rebatch build request: %v", err)), false
	}

	resp, err := p.gw.ProxyClient().Do(hreq)
	if err != nil {
		return failAll(calls, &rpc.Error{
			Status: http.StatusBadGateway, Code: "UPSTREAM_UNAVAILABLE",
			Message: fmt.Sprintf("rebatch %s: %v", addr, err),
		}), false
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		p.markUnsupported(addr)
		return nil, true
	}
	if resp.StatusCode >= 400 {
		return failAll(calls, &rpc.Error{
			Status: resp.StatusCode, Code: "UPSTREAM_UNAVAILABLE",
			Message: fmt.Sprintf("rebatch %s: status %d", addr, resp.StatusCode),
		}), false
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return failAll(calls, rpc.Internal("rebatch %s: read body: %v", addr, err)), false
	}
	var br gateway.BatchResponse
	if err := json.Unmarshal(body, &br); err != nil {
		return failAll(calls, rpc.Internal("rebatch %s: decode batch: %v", addr, err)), false
	}
	if br.Results == nil {
		br.Results = map[string]json.RawMessage{}
	}
	for alias := range calls {
		if _, ok := br.Results[alias]; !ok {
			br.Results[alias] = rpc.MarshalError(rpc.Internal("rebatch %s: missing result for alias %q", addr, alias))
		}
	}
	return br.Results, false
}

func failAll(calls map[string]gateway.BatchCall, err *rpc.Error) map[string]json.RawMessage {
	body := rpc.MarshalError(err)
	out := make(map[string]json.RawMessage, len(calls))
	for alias := range calls {
		out[alias] = body
	}
	return out
}

func (p *Plugin) unsupported(addr string) bool {
	p.muSupp.RLock()
	exp, ok := p.missing[addr]
	p.muSupp.RUnlock()
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		p.muSupp.Lock()
		delete(p.missing, addr)
		p.muSupp.Unlock()
		return false
	}
	return true
}

func (p *Plugin) markUnsupported(addr string) {
	if p.unsupportedTTL <= 0 {
		return
	}
	p.muSupp.Lock()
	p.missing[addr] = time.Now().Add(p.unsupportedTTL)
	p.muSupp.Unlock()
}
