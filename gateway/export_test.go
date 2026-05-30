package gateway

import "context"

// This file is compiled only into the gateway test binary. It lives in
// `package gateway` (not gateway_test) so it can reach the package's
// unexported internals, and exports thin bridges for the external
// gateway_test package (which dot-imports gateway). This is what lets
// the tests exercise the REAL builtin plugins without an import cycle.

// HandleRaw invokes the unexported per-request handler directly,
// bypassing the middleware chain — used by tests that want to assert
// raw routing behavior.
func (g *Gateway) HandleRaw(ctx context.Context, req *Request) *Response {
	return g.handle(ctx, req)
}

// RegisterField exposes the gateway's RegisterResolver field.
func (g *Gateway) RegisterField() *RegisterResolver { return g.register }

// AuthBindingForTest exposes the unexported authBinding field.
func (g *Gateway) AuthBindingForTest() *AuthBinding { return g.authBinding }

// AuthzBindingForTest exposes the unexported authzBinding field.
func (g *Gateway) AuthzBindingForTest() *AuthzBinding { return g.authzBinding }

// BindAuthRaw calls the strict (panic-on-duplicate) bindAuth.
func (g *Gateway) BindAuthRaw(service, method string) { g.bindAuth(service, method) }

// PluginHookView is a boolean snapshot of the sub-interfaces a single
// registered plugin entry satisfies. Each bool reports field != nil on
// the underlying pluginEntry.
type PluginHookView struct {
	Name             string
	HeaderInjector   bool
	HeaderParser     bool
	AuthTranslator   bool
	DispatchHook     bool
	BootValidator    bool
	LifecycleHook    bool
	IntroContributor bool
}

// PluginHookViews returns one PluginHookView per registered plugin.
func (g *Gateway) PluginHookViews() []PluginHookView {
	g.muPlugins.RLock()
	defer g.muPlugins.RUnlock()
	out := make([]PluginHookView, 0, len(g.plugins))
	for _, e := range g.plugins {
		out = append(out, PluginHookView{
			Name:             e.name,
			HeaderInjector:   e.headerInjector != nil,
			HeaderParser:     e.headerParser != nil,
			AuthTranslator:   e.authTranslator != nil,
			DispatchHook:     e.dispatchHook != nil,
			BootValidator:    e.bootValidator != nil,
			LifecycleHook:    e.lifecycleHook != nil,
			IntroContributor: e.introContributor != nil,
		})
	}
	return out
}
