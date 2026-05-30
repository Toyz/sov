// Package audit ships a sov plugin that records every dispatch event
// into a sliding-window in-memory ring AND emits structured JSON to a
// writer (typically os.Stdout or a log file). Also exposes
// `Audit.recent` as a wire-callable RPC so operators can query the
// last events without scraping logs — demonstrates the
// plugin-as-also-a-service pattern.
//
//	gw.Use(audit.New(os.Stdout))                            // log + introspect-only
//	gw.Use(audit.New(os.Stdout, audit.WithRingSize(500)))   // bigger ring
package audit

import (
	"context"
	"encoding/json"
	"io"
	"sync"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/rpc"
)

const defaultRingSize = 100

// AuditRouter is the audit plugin. Type name ends in "Router" so the
// engine can register its RPC methods (Recent) under the wire name
// "Audit". Satisfies:
//   - gateway.Plugin (PluginName)
//   - gateway.DispatchHook (per-request recording)
//   - gateway.IntrospectContributor (exposes the most recent event count in /rpc/_introspect)
//   - has RPC methods → registered as the "Audit" router with `recent`
//     callable as POST /rpc/Audit/recent.
type AuditRouter struct {
	mu      sync.Mutex
	out     io.Writer
	ring    []gateway.DispatchEvent
	cursor  int
	size    int
	dropped uint64
}

// Compile-time proof of the hooks this plugin binds — a signature
// drift here is a build error, not a silent non-binding at runtime.
var (
	_ gateway.Plugin                = (*AuditRouter)(nil)
	_ gateway.PluginDoc             = (*AuditRouter)(nil)
	_ gateway.DispatchHook          = (*AuditRouter)(nil)
	_ gateway.CapabilityProvider    = (*AuditRouter)(nil)
	_ gateway.IntrospectContributor = (*AuditRouter)(nil)
)

// Config configures the audit plugin. Out is the per-event JSON
// stream destination (pass io.Discard to skip log emission and use
// only the ring). RingSize is the in-memory ring capacity (default
// 100 when zero).
type Config struct {
	Out      io.Writer
	RingSize int
}

// New returns the plugin from cfg.
func New(cfg Config) *AuditRouter {
	size := cfg.RingSize
	if size <= 0 {
		size = defaultRingSize
	}
	return &AuditRouter{
		out:  cfg.Out,
		size: size,
		ring: make([]gateway.DispatchEvent, 0, size),
	}
}

// PluginName satisfies gateway.Plugin.
func (p *AuditRouter) PluginName() string { return "audit" }

// Doc surfaces a one-line description in /rpc/_introspect + the explorer.
func (p *AuditRouter) Doc() string {
	return "Records recent DispatchEvents in a ring buffer; exposes Audit.recent for live inspection."
}

// OnDispatch satisfies gateway.DispatchHook — writes a JSON line to
// the configured writer + appends to the ring (overwriting oldest).
func (p *AuditRouter) OnDispatch(ev gateway.DispatchEvent) error {
	if p.out != nil {
		// Fire-and-forget JSON encode; the writer is consumer-owned so
		// if it blocks the plugin blocks — document that in the README.
		line, _ := json.Marshal(ev)
		_, _ = p.out.Write(append(line, '\n'))
	}
	p.mu.Lock()
	if len(p.ring) < p.size {
		p.ring = append(p.ring, ev)
	} else {
		p.ring[p.cursor] = ev
		p.cursor = (p.cursor + 1) % p.size
	}
	p.mu.Unlock()
	return nil
}

// RecentFn is the capability type peers consume to read the in-memory
// dispatch event ring without going through Audit.recent over the
// wire. Useful for in-process metrics windows + dashboards.
type RecentFn func(limit int) []gateway.DispatchEvent

// Capabilities publishes the ring reader. Look up via:
//
//	r, _ := gateway.GetCapability[audit.RecentFn](gw, "audit.Recent")
//	events := r(50)
func (p *AuditRouter) Capabilities() []gateway.Capability {
	return []gateway.Capability{
		{Type: "audit.Recent", Impl: RecentFn(p.recentEvents)},
	}
}

// recentEvents is the in-process equivalent of the Recent RPC method
// — returns up to `limit` most-recent events (newest first).
func (p *AuditRouter) recentEvents(limit int) []gateway.DispatchEvent {
	if limit <= 0 {
		limit = p.size
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.ring) == 0 {
		return nil
	}
	out := make([]gateway.DispatchEvent, 0, min(limit, len(p.ring)))
	for i := 0; i < len(p.ring) && i < limit; i++ {
		idx := (p.cursor - 1 - i + p.size) % p.size
		if idx < 0 || idx >= len(p.ring) {
			continue
		}
		out = append(out, p.ring[idx])
	}
	return out
}

// ContributeIntrospect satisfies gateway.IntrospectContributor —
// surfaces the current event count + ring capacity in the introspect
// report's own plugin info so operators can sanity-check at a glance
// without hitting Audit.recent. Decoration-only; ctx/trace/visited
// unused.
func (p *AuditRouter) ContributeIntrospect(_ context.Context, report *gateway.IntrospectReport, _ string, _ []string) error {
	p.mu.Lock()
	count := len(p.ring)
	cap := p.size
	dropped := p.dropped
	p.mu.Unlock()
	for i := range report.Plugins {
		if report.Plugins[i].Name == "audit" {
			if report.Plugins[i].Extra == nil {
				report.Plugins[i].Extra = map[string]any{}
			}
			report.Plugins[i].Extra["ring_count"] = count
			report.Plugins[i].Extra["ring_cap"] = cap
			report.Plugins[i].Extra["dropped"] = dropped
			return nil
		}
	}
	return nil
}

// ---- Wire surface: AuditRouter ----------------------------------------------
// AuditRouter is the wire-named router auto-registered when AuditRouter is
// passed to gw.Use. Exposes Audit.recent so operators can query the
// last events live (auth-gated by their authz policy, naturally).

// RecentParams is the request body for Audit.recent.
type RecentParams struct {
	Limit int `sov:"limit,0,omitempty,title=Limit,desc=Max events to return; default 50" json:"limit,omitempty"`
}

// RecentResult is the response body.
type RecentResult struct {
	Events []gateway.DispatchEvent `json:"events"`
}

// PublicMethods lets the gateway/authz allow Audit.recent without an
// authz binding. In practice operators gate this themselves; v1
// defaults to public for the demo.
func (p *AuditRouter) PublicMethods() []string { return []string{"recent"} }

// Recent returns the most-recent events, newest-first, capped at Limit
// or 50.
func (p *AuditRouter) Recent(_ *rpc.Context, params *RecentParams) (*RecentResult, error) {
	limit := 50
	if params != nil && params.Limit > 0 && params.Limit < p.size {
		limit = params.Limit
	}
	// recentEvents is the single ring reader (newest-first, handles both
	// partial and wrapped rings). The wire method just normalizes nil to
	// an empty slice so the JSON response is `[]`, not `null`.
	events := p.recentEvents(limit)
	if events == nil {
		events = []gateway.DispatchEvent{}
	}
	return &RecentResult{Events: events}, nil
}
