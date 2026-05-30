package gateway

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
)

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
	if hi, ok := p.(HeaderInjector); ok {
		entry.headerInjector = hi
	}
	if hp, ok := p.(HeaderParser); ok {
		entry.headerParser = hp
	}
	if hc, ok := p.(HeaderClaimer); ok {
		raw := hc.ClaimedHeaders()
		canon := make([]string, 0, len(raw))
		for _, h := range raw {
			if h == "" {
				continue
			}
			canon = append(canon, http.CanonicalHeaderKey(h))
		}
		entry.headerClaims = canon
	}
	if at, ok := p.(AuthTranslator); ok {
		entry.authTranslator = at
	}
	if dh, ok := p.(DispatchHook); ok {
		entry.dispatchHook = dh
	}
	if bv, ok := p.(BootValidator); ok {
		entry.bootValidator = bv
	}
	if lh, ok := p.(LifecycleHook); ok {
		entry.lifecycleHook = lh
	}
	if ic, ok := p.(IntrospectContributor); ok {
		entry.introContributor = ic
	}
	if mw, ok := p.(Middlewarer); ok {
		entry.middlewarer = mw
	}
	if ca, ok := p.(ConfigApplier); ok {
		entry.configApplier = ca
	}
	if rh, ok := p.(RouteHandler); ok {
		entry.routeHandler = rh
	}
	if mc, ok := p.(MeshConflictPolicy); ok {
		entry.meshConflict = mc
	}
	if ut, ok := p.(UpstreamTrustPolicy); ok {
		entry.upstreamTrust = ut
	}
	if sv, ok := p.(SealVerifier); ok {
		entry.sealVerifier = sv
	}
	if ha, ok := p.(HealthAggregator); ok {
		entry.healthAggregator = ha
	}
	if r, ok := p.(Resolver); ok {
		entry.resolver = r
	}
	if s, ok := p.(Server); ok {
		entry.server = s
	}
	if cc, ok := p.(ContextContributor); ok {
		entry.ctxContributor = cc
	}
	if ri, ok := p.(ResponseInterceptor); ok {
		entry.respInterceptor = ri
	}
	if rh, ok := p.(RecoveryHandler); ok {
		entry.recoveryHandler = rh
	}
	if pd, ok := p.(PluginDependency); ok {
		entry.requires = pd.Requires()
		entry.after = pd.After()
	}
	if cp, ok := p.(CapabilityProvider); ok {
		entry.capabilities = cp.Capabilities()
	}
	if lg, ok := p.(Logger); ok {
		entry.logger = lg
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

	// Requires + After validation is deferred to ListenAndServe so
	// operators can Use plugins in any order. The framework topo-sorts
	// at boot — see Gateway.reorderPluginsByDependency. Use only
	// validates the entry has at least one capability.

	// Require at least one capability — empty Use() is a no-op bug.
	// satisfiedHooks() enumerates every sub-interface slot; reuse it as
	// the single source of truth so this guard can't drift from the set
	// of hooks when a new one is added.
	if !entry.hasRouter && len(entry.satisfiedHooks()) == 0 {
		return fmt.Errorf("gateway.Use: %s satisfies no plugin sub-interface and has no RPC methods", entry.name)
	}

	// ConfigApplier runs FIRST — it mutates gateway-owned state that
	// other hooks may read (e.g. setting the HMAC secret before any
	// HeaderInjector reads it).
	//
	// Contract: if Apply fails (HaltErr), Use returns the error and the
	// plugin is NOT added to g.plugins — it is "as if Use never ran". A
	// plugin that failed to configure itself must not appear half-wired in
	// the dispatch path or /rpc/_introspect.plugins. The append below is
	// reached only after Apply succeeds.
	if entry.configApplier != nil {
		_, bootErr, _ := g.safeHook("ConfigApplier", entry.name, func() error {
			return entry.configApplier.Apply(g)
		})
		if bootErr != nil {
			return bootErr
		}
	}

	g.muPlugins.Lock()
	g.plugins = append(g.plugins, entry)
	if entry.routeHandler != nil {
		for _, pat := range entry.routeHandler.RoutePatterns() {
			if pat == "" {
				continue
			}
			g.pluginRoutes = append(g.pluginRoutes, pluginRoute{
				pattern: pat,
				subtree: pat[len(pat)-1] == '/',
				handler: entry.routeHandler.ServeRoute,
				owner:   entry.name,
			})
		}
	}
	if entry.resolver != nil && g.resolverChain != nil {
		g.resolverChain.addPlugin(entry.resolver)
	}
	if entry.server != nil {
		// Swap the gateway's server pointer. Re-bind the dispatch
		// handler so the new server gets it; re-wire the trust guard
		// if the new server is the default NetHTTPServer + trust mode
		// was requested at construction.
		g.server = entry.server
		entry.server.Handle(func(ctx context.Context, req *Request) *Response {
			return g.dispatch(ctx, req)
		})
		if ns, ok := entry.server.(*NetHTTPServer); ok && g.trustUpstreamWired {
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

	if entry.middlewarer != nil {
		g.UseMiddleware(entry.middlewarer.Wrap)
	}

	return nil
}

// hasRouterShape mirrors rpc.Engine.Register's acceptance test —
// reports whether v is a pointer-to-struct whose type name ends in
// "Router" AND has at least one exported method. The engine itself
// re-validates signatures and panics on mismatch; this is just a
// gate so non-router plugins don't get pushed through Register.
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
