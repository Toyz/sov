package gateway

// pluginEntry is the internal record per-Use registration. It holds only the
// plugin value plus the METADATA computed at Use time. Hook dispatch does not
// read this struct — it discovers implementers generically via
// PluginsImplementing[T] (plugin_discover.go), so the core stores no per-hook
// interface slots.
type pluginEntry struct {
	any          any    // the original value passed to Use(); what PluginByName returns
	name         string // Plugin.PluginName(), or a synthesized type label
	doc          string // PluginDoc.Doc(), surfaced in PluginInfo.Extra["doc"]
	requires     []string
	after        []string
	capabilities []Capability
	hasRouter    bool
}

// frameworkHook names a built-in gateway hook interface and how to test for it.
// This table exists ONLY to populate PluginInfo.Hooks for introspection — it is
// NOT how hooks are dispatched. Dispatch discovers implementers generically, so
// a plugin can satisfy an interface absent from this table (e.g. a builtin's own
// extension interface) and still work; it simply won't be named in the hooks
// list. Order is stable for deterministic output.
type frameworkHook struct {
	name string
	is   func(any) bool
}

var frameworkHooks = []frameworkHook{
	{"HeaderInjector", func(p any) bool { _, ok := p.(HeaderInjector); return ok }},
	{"HeaderParser", func(p any) bool { _, ok := p.(HeaderParser); return ok }},
	{"HeaderClaimer", func(p any) bool { _, ok := p.(HeaderClaimer); return ok }},
	{"AuthTranslator", func(p any) bool { _, ok := p.(AuthTranslator); return ok }},
	{"DispatchHook", func(p any) bool { _, ok := p.(DispatchHook); return ok }},
	{"BootValidator", func(p any) bool { _, ok := p.(BootValidator); return ok }},
	{"LifecycleHook", func(p any) bool { _, ok := p.(LifecycleHook); return ok }},
	{"IntrospectContributor", func(p any) bool { _, ok := p.(IntrospectContributor); return ok }},
	{"Middlewarer", func(p any) bool { _, ok := p.(Middlewarer); return ok }},
	{"ConfigApplier", func(p any) bool { _, ok := p.(ConfigApplier); return ok }},
	{"RouteHandler", func(p any) bool { _, ok := p.(RouteHandler); return ok }},
	{"MeshConflictPolicy", func(p any) bool { _, ok := p.(MeshConflictPolicy); return ok }},
	{"UpstreamTrustPolicy", func(p any) bool { _, ok := p.(UpstreamTrustPolicy); return ok }},
	{"SealVerifier", func(p any) bool { _, ok := p.(SealVerifier); return ok }},
	{"HealthAggregator", func(p any) bool { _, ok := p.(HealthAggregator); return ok }},
	{"ReadinessContributor", func(p any) bool { _, ok := p.(ReadinessContributor); return ok }},
	{"Resolver", func(p any) bool { _, ok := p.(Resolver); return ok }},
	{"Server", func(p any) bool { _, ok := p.(Server); return ok }},
	{"ContextContributor", func(p any) bool { _, ok := p.(ContextContributor); return ok }},
	{"ResponseInterceptor", func(p any) bool { _, ok := p.(ResponseInterceptor); return ok }},
	{"RecoveryHandler", func(p any) bool { _, ok := p.(RecoveryHandler); return ok }},
	{"CapabilityProvider", func(p any) bool { _, ok := p.(CapabilityProvider); return ok }},
	{"Logger", func(p any) bool { _, ok := p.(Logger); return ok }},
}

// satisfiedHooks lists the built-in hook interfaces this plugin implements, for
// introspection display (PluginInfo.Hooks). Best-effort — see frameworkHooks.
func (e *pluginEntry) satisfiedHooks() []string {
	var out []string
	for _, h := range frameworkHooks {
		if h.is(e.any) {
			out = append(out, h.name)
		}
	}
	return out
}
