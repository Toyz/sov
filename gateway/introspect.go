package gateway

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Toyz/sov/rpc"
)

// ---------------------------------------------------------------------------
// _introspect
// ---------------------------------------------------------------------------

// IntrospectReport is the aggregated /_introspect JSON body.
//
// Services keys each declared router → its descriptors (one or more
// when the same name appears in multiple pods — drift!).
//
// Types flattens every Go type appearing in any service's request or
// response into one map. UsedBy on each TypeDescriptor records every
// (service, method, role) the type appears in.
//
// CrossRefs is the drift radar: any type name that surfaced in
// multiple services with differing ShapeHashes lands here, grouped by
// hash. Empty when every consumer agrees on a single shape.
type IntrospectReport struct {
	Services  map[string][]rpc.RouterDescriptor `json:"services"`
	Types     map[string]TypeDescriptor         `json:"types"`
	CrossRefs map[string]TypeVariants           `json:"cross_refs"`
	// Plugins lists every plugin registered on this gateway via
	// gw.Use(). Populated for the ROOT introspect call only;
	// federated probes skip it so per-tier visibility stays clean.
	Plugins []PluginInfo `json:"plugins,omitempty"`
	// BoundaryWarnings flags data-ownership smells found while inferring
	// type ownership — currently, any type returned by more than one
	// service (ambiguous producer). Empty when boundaries are clean.
	BoundaryWarnings []string `json:"boundary_warnings,omitempty"`
}

// catalogCache holds a marshalled IntrospectReport with a 30s TTL.
// Invalidated by RegisterResolver mutations so newly-joined pods show
// up immediately (no stale 30s window after a deploy).
type catalogCache struct {
	mu        sync.Mutex
	expiresAt time.Time
	body      []byte // public payload — soft-internal methods omitted
	bodyFull  []byte // full payload — soft-internal methods present, flagged internal:true
}

const catalogTTL = 30 * time.Second

// Introspect cascade headers — propagate a request id + the set of
// visited gateway addresses so federated cycles short-circuit and
// duplicate probes are deduped.
const (
	IntrospectTraceHeader   = "X-Sov-Introspect-Trace"
	IntrospectVisitedHeader = "X-Sov-Introspect-Visited"
	// IntrospectInternalHeader, when set to "1" on a root /rpc/_introspect
	// request, returns the FULL catalog including SOFT-hidden methods
	// (flagged internal:true). Hard-hidden methods are never returned.
	// The explorer's "show internal" toggle sets it.
	IntrospectInternalHeader = "X-Sov-Introspect-Internal"
)

func (g *Gateway) handleIntrospect(ctx context.Context, req *Request) *Response {
	// Loop-guard: when this gateway is itself being probed by an
	// upstream master, the inbound Visited header lists every gateway
	// already seen this round. We add ourselves and forward it on the
	// fan-out so a downstream that points back at us bails clean.
	trace := ""
	visited := []string{}
	wantInternal := false
	if req != nil {
		trace = req.Header.Get(IntrospectTraceHeader)
		wantInternal = req.Header.Get(IntrospectInternalHeader) == "1"
		if v := req.Header.Get(IntrospectVisitedHeader); v != "" {
			for _, addr := range strings.Split(v, ",") {
				addr = strings.TrimSpace(addr)
				if addr != "" {
					visited = append(visited, addr)
				}
			}
		}
	}

	// Cache hit only when this is the ROOT call (no inbound trace).
	// Federated probes always re-build so their downstream visited list
	// is honored. The internal header selects the full payload; both
	// bodies are populated together so bodyFull is non-nil whenever body is.
	if trace == "" {
		g.catalog.mu.Lock()
		if g.catalog.body != nil && time.Now().Before(g.catalog.expiresAt) {
			body := g.catalog.body
			if wantInternal {
				body = g.catalog.bodyFull
			}
			g.catalog.mu.Unlock()
			return &Response{Status: 200, Body: body}
		}
		g.catalog.mu.Unlock()
		trace = generateTraceID()
	}

	report := g.buildIntrospectReport(ctx, trace, visited)

	// On the root call, populate Plugins BEFORE running contributors
	// so decorator-style contributors (audit ring count, metrics
	// snapshot) can stamp Extra fields on their own PluginInfo entry.
	// Federated probes skip Plugins to keep per-tier visibility clean.
	if len(visited) == 0 {
		report.Plugins = g.PluginInfos()
	}

	// IntrospectContributor plugins handle BOTH the remote fan-out
	// (registry aggregator merges /rpc/_introspect across pods) AND
	// local decoration (audit ring stats, metrics snapshot). One
	// iteration covers both — decorators conventionally guard on
	// len(visited)==0 themselves if they only want to stamp on the
	// root call.
	g.callIntrospectContributors(ctx, &report, trace, visited)

	// Strip HARD-hidden methods (auth/authz framework hooks + any
	// author HardHiddenMethods()/sov:"internal,hard") from the merged
	// report BEFORE building the type catalog, so neither payload — nor
	// the types used only by them — ever surfaces them.
	g.stripHardHidden(&report)

	// Build two payloads from one report: public (SOFT-internal methods
	// omitted) and full (retained, flagged internal:true). The type
	// catalog is rebuilt per payload so public.Types excludes types
	// referenced only by soft-hidden methods. Federated probes
	// (len(visited) > 0) only ever return public — soft internals never
	// leak upstream.
	pub, full := splitInternal(report)
	BuildTypeCatalog(&pub)
	pubBody, _ := json.Marshal(pub)

	var fullBody []byte
	if len(visited) == 0 {
		BuildTypeCatalog(&full)
		fullBody, _ = json.Marshal(full)

		g.catalog.mu.Lock()
		g.catalog.body = pubBody
		g.catalog.bodyFull = fullBody
		g.catalog.expiresAt = time.Now().Add(catalogTTL)
		g.catalog.mu.Unlock()

		if wantInternal {
			return &Response{Status: 200, Body: fullBody}
		}
	}

	return &Response{Status: 200, Body: pubBody}
}

// PluginInfos returns the PluginInfo list for this gateway's registered
// plugins. Order matches registration order so operators can read the
// wiring sequence directly. Exported so plugins (e.g. manifest) can read
// it without a /rpc/_introspect HTTP round trip.
func (g *Gateway) PluginInfos() []PluginInfo {
	g.muPlugins.RLock()
	snap := g.plugins
	g.muPlugins.RUnlock()
	out := make([]PluginInfo, 0, len(snap))
	for _, e := range snap {
		caps := make([]string, 0, len(e.capabilities))
		for _, c := range e.capabilities {
			caps = append(caps, c.Type)
		}
		info := PluginInfo{
			Name:         e.name,
			Hooks:        e.satisfiedHooks(),
			HasRouter:    e.hasRouter,
			Requires:     e.requires,
			After:        e.after,
			Capabilities: caps,
		}
		if e.doc != "" {
			info.Extra = map[string]any{"doc": e.doc}
		}
		out = append(out, info)
	}
	return out
}

// logPluginBindings emits, at Debug, the hooks each plugin actually bound
// — the antidote to silent duck-typed non-binding. If you wrote a hook
// and it isn't in this list, your method signature didn't match the
// interface (the assertion never fired). Pair with the
// `var _ gateway.X = (*Plugin)(nil)` compile-time assertions to catch the
// same drift at build time.
func (g *Gateway) logPluginBindings() {
	log := g.Log()
	for _, p := range g.PluginInfos() {
		hooks := append([]string{}, p.Hooks...)
		if p.HasRouter {
			hooks = append(hooks, "RPCRouter")
		}
		if len(hooks) == 0 {
			hooks = []string{"(none)"}
		}
		log.Debug("gateway: plugin wired", "plugin", p.Name, "hooks", hooks)
	}
}

// generateTraceID returns a short id used to dedupe diamond fan-outs
// across the introspect cascade. Time-based (cheap, no crypto/rand
// dep here) — collisions are harmless because the visited list is
// the primary loop guard.
// GenerateIntrospectTraceID returns a short id used to dedupe diamond
// fan-outs across the introspect cascade. Exported for plugin
// aggregators that originate a cascade from a non-root call.
func GenerateIntrospectTraceID() string { return generateTraceID() }

func generateTraceID() string {
	return "tr-" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

// invalidateCatalog clears the cached _introspect body so the next
// request rebuilds. Wired to the RegisterResolver's onChange hook so
// pod register/expire events show up immediately.
func (g *Gateway) invalidateCatalog() {
	g.catalog.mu.Lock()
	g.catalog.body = nil
	g.catalog.bodyFull = nil
	g.catalog.expiresAt = time.Time{}
	g.catalog.mu.Unlock()
}

// buildIntrospectReport builds the LOCAL-only catalog from this
// gateway's in-process engine. Remote fan-out (federation, mesh) is
// performed by registered IntrospectContributor plugins;
// handleIntrospect calls them after this returns and BEFORE
// BuildTypeCatalog so the types map covers every merged service
// descriptor.
func (g *Gateway) buildIntrospectReport(_ context.Context, _ string, _ []string) IntrospectReport {
	out := IntrospectReport{
		Services:  map[string][]rpc.RouterDescriptor{},
		Types:     map[string]TypeDescriptor{},
		CrossRefs: map[string]TypeVariants{},
	}
	for _, rd := range g.engine.Describe() {
		out.Services[rd.Router] = []rpc.RouterDescriptor{rd}
	}
	return out
}

// stripHardHidden removes every HARD-hidden method from the merged
// report so it appears in NO introspect payload: (a) the bound auth
// `verify` / authz `check` framework hooks (matched against this
// aggregating gateway's bindings, covering local and remote auth pods),
// and (b) any method an author declared via HardHiddenMethods() or the
// `sov:"internal,hard"` sentinel (md.HardHidden, stamped at the origin
// gateway's Describe()). Routers left empty are dropped.
//
// SECURITY: this removes discoverability only. The methods stay
// dispatchable; authz governs who may call them.
func (g *Gateway) stripHardHidden(report *IntrospectReport) {
	isHook := func(router, method string) bool {
		if g.authBinding != nil && g.authBinding.Service == router && g.authBinding.Method == method {
			return true
		}
		if g.authzBinding != nil && g.authzBinding.Service == router && g.authzBinding.Method == method {
			return true
		}
		return false
	}
	for svc, rds := range report.Services {
		kept := make([]rpc.RouterDescriptor, 0, len(rds))
		for _, rd := range rds {
			// Methodless placeholder descriptors (e.g. aggregated markers)
			// pass through untouched — only drop a router we emptied by
			// removing methods.
			if len(rd.Methods) == 0 {
				kept = append(kept, rd)
				continue
			}
			rd2 := rd
			rd2.Methods = make([]rpc.MethodDescriptor, 0, len(rd.Methods))
			for _, md := range rd.Methods {
				if md.HardHidden || isHook(rd.Router, md.Method) {
					continue
				}
				rd2.Methods = append(rd2.Methods, md)
			}
			if len(rd2.Methods) > 0 {
				kept = append(kept, rd2)
			}
		}
		if len(kept) > 0 {
			report.Services[svc] = kept
		} else {
			delete(report.Services, svc)
		}
	}
}

// splitInternal derives the two introspect payloads from a (hard-hidden
// already stripped) report. full retains SOFT-internal methods (flagged
// internal:true); public omits them and drops emptied routers. Each gets
// its own fresh Types/CrossRefs so a subsequent BuildTypeCatalog excludes
// (public) or includes (full) types referenced only by soft methods.
func splitInternal(report IntrospectReport) (public, full IntrospectReport) {
	full = report
	full.Types = map[string]TypeDescriptor{}
	full.CrossRefs = map[string]TypeVariants{}
	full.BoundaryWarnings = nil

	public = report
	public.Types = map[string]TypeDescriptor{}
	public.CrossRefs = map[string]TypeVariants{}
	public.BoundaryWarnings = nil
	public.Services = map[string][]rpc.RouterDescriptor{}
	for svc, rds := range report.Services {
		kept := make([]rpc.RouterDescriptor, 0, len(rds))
		for _, rd := range rds {
			if len(rd.Methods) == 0 {
				kept = append(kept, rd)
				continue
			}
			rd2 := rd
			rd2.Methods = make([]rpc.MethodDescriptor, 0, len(rd.Methods))
			for _, md := range rd.Methods {
				if md.Internal {
					continue
				}
				rd2.Methods = append(rd2.Methods, md)
			}
			if len(rd2.Methods) > 0 {
				kept = append(kept, rd2)
			}
		}
		if len(kept) > 0 {
			public.Services[svc] = kept
		}
	}
	return public, full
}
