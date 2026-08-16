package gateway

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
)

// UseAll calls Use on each plugin in order. First error stops the
// chain so config-applying plugins that need to succeed before later
// plugins fail fast. Preset packages return []any slices that pair
// with this helper:
//
//	gw := sov.New()
//	if err := gw.UseAll(preset.Monolith()...); err != nil {
//	    log.Fatal(err)
//	}
func (g *Gateway) UseAll(plugins ...any) error {
	for _, p := range plugins {
		if err := g.Use(p); err != nil {
			return err
		}
	}
	return nil
}

// MustUseAll is the panicking variant of UseAll for main() callers
// that would otherwise unwrap with log.Fatal. Panics with the wrapped
// error so the program exits with a stack trace.
func (g *Gateway) MustUseAll(plugins ...any) {
	if err := g.UseAll(plugins...); err != nil {
		panic(fmt.Sprintf("gateway.MustUseAll: %v", err))
	}
}

// MustUse is the panicking variant of Use.
func (g *Gateway) MustUse(p any) {
	if err := g.Use(p); err != nil {
		panic(fmt.Sprintf("gateway.MustUse: %v", err))
	}
}

// Use registers a plugin on the gateway. The argument is `any` because
// plugins are duck-typed: the gateway checks each sub-interface
// (HeaderInjector, AuthTranslator, …) via Go interface assertion and
// stashes pointers in the appropriate slot list.
//
// If the plugin ALSO has RPC-shaped methods (matching the same
// signature contract gw.Register requires), they are registered on the
// engine — one Use call yields both extension hooks and wire-callable
// methods.
//
// Use is safe to call before or after ListenAndServe. Plugins added
// post-start participate in subsequent requests; existing in-flight
// requests are not retroactively wrapped.
//
// Returns an error when the plugin satisfies NO sub-interface AND has
// no RPC methods — that's almost certainly a bug (the caller probably
// forgot to make the methods exported, or passed the wrong type).
func (g *Gateway) Use(p any) error {
	if p == nil {
		return fmt.Errorf("gateway.Use: plugin is nil")
	}

	entry := &pluginEntry{any: p}
	if named, ok := p.(Plugin); ok {
		entry.name = named.PluginName()
	}
	if doc, ok := p.(PluginDoc); ok {
		entry.doc = doc.Doc()
	}
	// Only METADATA is precomputed at Use time. The dispatch fan-outs discover
	// their implementers generically via PluginsImplementing[T] (plugin_discover.go),
	// so the core no longer catalogues every hook interface — the plugin owns its
	// interfaces, and a subsystem asks for the ones it needs. Interfaces with a
	// Use-time SIDE EFFECT (ConfigApplier, RouteHandler, Resolver, Server,
	// Middlewarer) are asserted inline below where that effect happens.
	if pd, ok := p.(PluginDependency); ok {
		entry.requires = pd.Requires()
		entry.after = pd.After()
	}
	if cp, ok := p.(CapabilityProvider); ok {
		entry.capabilities = cp.Capabilities()
	}

	// Detect RPC-method router shape. Same rule rpc.Engine.Register
	// applies: pointer to a struct whose type name ends in "Router"
	// AND that has at least one exported RPC-shaped method. We don't
	// recheck the signatures here; engine.Register panics on shape
	// mismatches the same as before.
	if hasRouterShape(p) {
		entry.hasRouter = true
		g.engine.Register(p)
		// Mirror gateway.Register's role auto-bind so AuthService /
		// AuthzService detection works when callers go through Use
		// instead of Register directly.
		g.autoBindRoles(p)
	}

	if entry.name == "" {
		// Synthesize a label from the Go type so the plugin still
		// shows up in the introspect plugins list with a usable name.
		entry.name = goTypeLabel(p)
	}

	// Reject a plugin that exposes NOTHING — no exported methods and no RPC
	// router. It can't satisfy any interface (a core hook OR a builtin's own
	// extension interface), so it's a wiring bug. This is method-based, not a
	// fixed interface list: implementing ANY interface means having methods.
	if !entry.hasRouter && !hasAnyExportedMethod(p) {
		return fmt.Errorf("gateway.Use: %s exposes no methods and no RPC router — it can satisfy no plugin interface", entry.name)
	}

	// Requires + After validation is deferred to ListenAndServe so operators can
	// Use plugins in any order (topo-sorted at boot — reorderPluginsByDependency).
	//
	// There is deliberately NO "must implement a known hook" gate: a plugin may
	// implement only a builtin's OWN interface (e.g. an explorer extension) the
	// core knows nothing about, and it must still register. An inert plugin is
	// harmless — it simply never matches any PluginsImplementing query.

	// ConfigApplier runs FIRST — it mutates gateway-owned state that other hooks
	// may read (e.g. the HMAC secret). If Apply fails, Use returns the error and
	// the plugin is NOT added, so a mis-configured plugin never appears half-wired.
	if ca, ok := p.(ConfigApplier); ok {
		_, bootErr, _ := g.safeHook("ConfigApplier", entry.name, func() error {
			return ca.Apply(g)
		})
		if bootErr != nil {
			return bootErr
		}
	}

	g.muPlugins.Lock()
	g.plugins = append(g.plugins, entry)
	g.hookCache = nil // plugin set changed — drop memoized PluginsImplementing results
	if rh, ok := p.(RouteHandler); ok {
		// Optional explicit ordering: a RoutePrioritizer overrides specificity
		// (higher priority wins over a longer pattern).
		priority := 0
		if rp, ok := rh.(RoutePrioritizer); ok {
			priority = rp.RoutePriority()
		}
		for _, pat := range rh.RoutePatterns() {
			if pat == "" {
				continue
			}
			g.pluginRoutes = append(g.pluginRoutes, pluginRoute{
				pattern:  pat,
				subtree:  pat[len(pat)-1] == '/',
				handler:  rh.ServeRoute,
				owner:    entry.name,
				priority: priority,
			})
		}
	}
	if r, ok := p.(Resolver); ok && g.resolverChain != nil {
		g.resolverChain.addPlugin(r)
	}
	if s, ok := p.(Server); ok {
		// Swap the gateway's server; re-bind dispatch + re-wire the trust guard
		// when it's the default NetHTTPServer and trust mode was requested.
		g.server = s
		s.Handle(func(ctx context.Context, req *Request) *Response {
			return g.dispatch(ctx, req)
		})
		if ns, ok := s.(*NetHTTPServer); ok && g.trustUpstreamWired {
			ns.SetTrustGuard(func(r *http.Request, _ []byte) bool {
				if !g.upstreamTrusted(r.Header) {
					return false
				}
				if !g.sealValid(r.Header) {
					return false
				}
				return true
			})
		}
	}
	g.muPlugins.Unlock()

	if mw, ok := p.(Middlewarer); ok {
		g.UseMiddleware(mw.Wrap)
	}

	return nil
}

// hasRouterShape mirrors rpc.Engine.Register's acceptance test —
// reports whether v is a pointer-to-struct whose type name ends in
// "Router" AND has at least one exported method. The engine itself
// re-validates signatures and panics on mismatch; this is just a
// gate so non-router plugins don't get pushed through Register.
// hasAnyExportedMethod reports whether v's type has at least one exported method
// — i.e. it can satisfy some interface. Interface-agnostic, so a plugin
// implementing an interface the core has never heard of still passes.
func hasAnyExportedMethod(v any) bool {
	t := reflect.TypeOf(v)
	if t == nil {
		return false
	}
	for i := 0; i < t.NumMethod(); i++ {
		if t.Method(i).IsExported() {
			return true
		}
	}
	return false
}

func hasRouterShape(v any) bool {
	if v == nil {
		return false
	}
	t := reflect.TypeOf(v)
	if t.Kind() != reflect.Ptr {
		return false
	}
	elem := t.Elem()
	if elem.Kind() != reflect.Struct {
		return false
	}
	name := elem.Name()
	if name == "" || len(name) < len("Router") || name[len(name)-len("Router"):] != "Router" {
		return false
	}
	for i := 0; i < t.NumMethod(); i++ {
		if t.Method(i).IsExported() {
			return true
		}
	}
	return false
}

// goTypeLabel returns a "*pkg.TypeName" style label for diagnostics.
func goTypeLabel(v any) string {
	t := reflect.TypeOf(v)
	if t == nil {
		// Unreachable today — the sole caller (Use) rejects nil before
		// reaching here. Kept so the helper never panics if reused.
		return "<nil>"
	}
	return t.String()
}
