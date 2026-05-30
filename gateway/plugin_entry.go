package gateway

// pluginEntry is the internal record per-Use registration. Holds
// pointer-typed references to every sub-interface the plugin
// satisfies so the dispatch hot path does no further reflection.
type pluginEntry struct {
	any              any // the original value passed to Use(); what PluginByName returns
	headerInjector   HeaderInjector
	headerParser     HeaderParser
	headerClaims     []string
	authTranslator   AuthTranslator
	dispatchHook     DispatchHook
	bootValidator    BootValidator
	lifecycleHook    LifecycleHook
	introContributor IntrospectContributor
	middlewarer      Middlewarer
	configApplier    ConfigApplier
	routeHandler     RouteHandler
	meshConflict     MeshConflictPolicy
	upstreamTrust    UpstreamTrustPolicy
	sealVerifier     SealVerifier
	healthAggregator HealthAggregator
	resolver         Resolver
	server           Server
	ctxContributor   ContextContributor
	respInterceptor  ResponseInterceptor
	recoveryHandler  RecoveryHandler
	requires         []string
	after            []string
	capabilities     []Capability
	logger           Logger
	hasRouter        bool
	name             string
	doc              string // PluginDoc.Doc() output, surfaced in PluginInfo.Extra["doc"]
}

// satisfiedHooks returns the human-readable list of sub-interfaces
// this entry implements — drives PluginInfo.Hooks.
func (e *pluginEntry) satisfiedHooks() []string {
	var out []string
	if e.headerInjector != nil {
		out = append(out, "HeaderInjector")
	}
	if e.headerParser != nil {
		out = append(out, "HeaderParser")
	}
	if e.authTranslator != nil {
		out = append(out, "AuthTranslator")
	}
	if e.dispatchHook != nil {
		out = append(out, "DispatchHook")
	}
	if e.bootValidator != nil {
		out = append(out, "BootValidator")
	}
	if e.lifecycleHook != nil {
		out = append(out, "LifecycleHook")
	}
	if e.introContributor != nil {
		out = append(out, "IntrospectContributor")
	}
	if e.middlewarer != nil {
		out = append(out, "Middlewarer")
	}
	if e.configApplier != nil {
		out = append(out, "ConfigApplier")
	}
	if e.routeHandler != nil {
		out = append(out, "RouteHandler")
	}
	if e.meshConflict != nil {
		out = append(out, "MeshConflictPolicy")
	}
	if e.upstreamTrust != nil {
		out = append(out, "UpstreamTrustPolicy")
	}
	if e.sealVerifier != nil {
		out = append(out, "SealVerifier")
	}
	if e.healthAggregator != nil {
		out = append(out, "HealthAggregator")
	}
	if e.resolver != nil {
		out = append(out, "Resolver")
	}
	if e.server != nil {
		out = append(out, "Server")
	}
	if e.ctxContributor != nil {
		out = append(out, "ContextContributor")
	}
	if e.respInterceptor != nil {
		out = append(out, "ResponseInterceptor")
	}
	if e.recoveryHandler != nil {
		out = append(out, "RecoveryHandler")
	}
	return out
}
