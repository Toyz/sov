package gateway

import (
	"context"
	"net/http"
	"time"

	"github.com/Toyz/sov/rpc"
)

// Plugin is the optional diagnostic marker. Types that implement at
// least one sub-interface (HeaderInjector, AuthTranslator, etc.) are
// treated as plugins even if they don't satisfy Plugin — but
// implementing it adds a human-readable name to the introspect
// "plugins" array and the explorer's Plugins tab.
type Plugin interface {
	PluginName() string
}

// PluginDoc is an optional companion to Plugin — implement when you
// want a one-paragraph description to surface in /rpc/_introspect's
// plugin entry (Extra["doc"]) and in `sov inspect` output. Keep it
// short; multi-line text is fine but the explorer renders inline.
//
//	func (p *Plugin) Doc() string {
//	    return "Stamps X-Sov-Request-Id on every request and propagates it across hops."
//	}
type PluginDoc interface {
	Doc() string
}

// ---------------------------------------------------------------------------
// Header-hook cluster — three independent interfaces that all touch HTTP
// headers, grouped here for documentation. The framework type-asserts on
// each inner name individually so a plugin implements only the subset it
// needs:
//   - InjectHeaders:  add headers on outbound proxy hops (HeaderInjector)
//   - ParseHeaders:   read inbound headers after trust-strip (HeaderParser)
//   - ClaimedHeaders: declare header names that bypass the identity-strip (HeaderClaimer)
// ---------------------------------------------------------------------------

// HeaderInjector fires before every OUTBOUND proxy hop
// (dispatchRemote, dispatchRemoteBatch, federated introspect/health
// probes). Add headers to hreq; do NOT mutate ctx or body.
type HeaderInjector interface {
	InjectHeaders(ctx context.Context, req *Request, hreq *http.Request) error
}

// HeaderParser fires on every INBOUND request, after the trust guard
// has decided whether to keep X-Sov-* headers. Read non-standard
// headers off req.Header and stash interpreted values on req.User /
// req.Header via mutation. Returning a non-nil *rpc.Error
// short-circuits dispatch with that error — use sparingly; most
// parsers should be lossy.
type HeaderParser interface {
	ParseHeaders(req *Request) *rpc.Error
}

// HeaderClaimer lets a plugin declare which inbound HTTP header names
// it OWNS — those headers bypass the framework's X-Sov-* identity-
// strip and reach req.Header intact. Without this, a plugin that
// reads e.g. X-Sov-Register-Sig (mesh-secret) would never see it
// because the edge strip nukes every X-Sov-*.
//
// Names are http.CanonicalHeaderKey-compared. Identity headers
// (X-Sov-Subject/Issuer/Scopes/Expires/Seal) cannot be claimed —
// the strip honors anti-smuggling regardless. Other X-Sov-* and
// arbitrary-named headers are passable.
//
//	func (p *Plugin) ClaimedHeaders() []string {
//	    return []string{"X-Sov-Register-Sig", "X-Sov-Register-Ts"}
//	}
type HeaderClaimer interface {
	ClaimedHeaders() []string
}

// AuthTranslator fires AFTER the auth middleware has resolved Claims
// (when it has). Translate the verified identity into any format your
// brownfield downstreams expect. Mutate req.Header to add legacy
// headers; the gateway forwards them on the proxy hop.
//
// Claims may be nil for anonymous requests — translators that need an
// identity should early-return in that case.
type AuthTranslator interface {
	TranslateAuth(req *Request, claims *Claims) error
}

// DispatchHook fires AFTER a handler returns (success or error). Sees
// the resolved router/method, status, duration, and identity. Runs on
// the dispatch goroutine; long-running work belongs on a buffered
// chan the plugin owns (otherwise you slow down every request).
type DispatchHook interface {
	OnDispatch(ev DispatchEvent) error
}

// DispatchEvent is the post-handler observation the gateway emits to
// every DispatchHook. JSON-tagged so plugins can marshal events
// directly without further translation.
type DispatchEvent struct {
	Router    string        `json:"router"`
	Method    string        `json:"method"`
	Path      string        `json:"path"`
	Status    int           `json:"status"`
	Duration  time.Duration `json:"duration_ns"`
	Subject   string        `json:"subject,omitempty"`
	ErrorCode string        `json:"error_code,omitempty"`
	BatchID   string        `json:"batch_id,omitempty"`
	At        time.Time     `json:"at"`
	// Mode records where the call actually ran. PEMM observability —
	// audit + metrics + tracing can split rates by where the work
	// landed without instrumenting the dispatcher itself.
	//
	// Values:
	//   "local"     — in-process router method handled it
	//   "remote"    — proxied to a single remote address
	//   "federated" — proxied via an intermediate gateway
	//   "framework" — framework endpoint (/rpc/_health, _introspect, …)
	//   "plugin"    — handled by a RouteHandler plugin
	//   ""          — pre-dispatch reject (404, path validation, etc.)
	Mode string `json:"mode,omitempty"`
}

// Dispatch mode labels for DispatchEvent.Mode / Response.Mode. Use these
// constants instead of the bare string literals at the dispatch call sites.
const (
	ModeLocal     = "local"     // in-process router method handled it
	ModeRemote    = "remote"    // proxied to a single remote address
	ModeFederated = "federated" // proxied via an intermediate gateway
	ModePeer      = "peer"      // dispatched to a linked in-process peer
	ModeFramework = "framework" // framework endpoint (_health, _introspect, …)
	ModePlugin    = "plugin"    // handled by a RouteHandler plugin
)

// BootValidator fires once at gw.ListenAndServe entry. Return an
// error to refuse startup with a clear message — replaces the
// boot-panic pattern (e.g. TrustUpstreamClaims-without-seal panic
// becomes a validator on the trust-guard plugin).
type BootValidator interface {
	ValidateBoot(g *Gateway) error
}

// LifecycleHook fires on gw.ListenAndServe start/stop. Use for
// background goroutines, connection pools, shutdown drains. OnStart
// runs after BootValidator passes; OnStop runs on context cancel.
type LifecycleHook interface {
	OnStart(ctx context.Context) error
	OnStop(ctx context.Context) error
}

// IntrospectContributor lets a plugin contribute to /rpc/_introspect.
// Fires after the framework builds the local report. Plugins may:
//   - decorate the report with their own block (cache stats, ring state)
//   - fan out to remote pods and merge their descriptors
//
// Use ctx and the cascade headers (trace, visited) for loop guarding
// when probing remotes. visited carries the normalized address list of
// gateways already visited on this introspect cascade; plugins MUST
// append themselves before fanning out and MUST skip any address
// already in visited.
//
// Replaces the prior IntrospectAugmenter (no-arg, local decoration)
// and IntrospectAggregator (ctx + cascade headers, remote fan-out)
// interfaces — both folded under one name. Plugins that don't need
// ctx/trace/visited just ignore those args.
type IntrospectContributor interface {
	ContributeIntrospect(ctx context.Context, report *IntrospectReport, trace string, visited []string) error
}

// Middlewarer is the new home for plugins that wrap the dispatch
// chain. Equivalent to today's Middleware closure but reachable
// through gw.Use so it shows up in the plugin list. Existing
// Middleware closures continue to work via the legacy
// WithMiddleware option.
type Middlewarer interface {
	Wrap(next Handler) Handler
}

// ConfigApplier lets a plugin mutate the gateway's framework-owned
// configuration at registration time. Fires synchronously inside
// gw.Use BEFORE any other hook is registered, so a config-applying
// plugin (mesh secret, allowlist, etc.) takes effect for every
// subsequent request — including ones served while later plugins are
// still being registered.
type ConfigApplier interface {
	Apply(g *Gateway) error
}

// MeshConflictPolicy decides whether an inbound /rpc/_register may
// take over an existing claim. Two paths share the interface:
//   - Role takeover: name already bound to an auth/authz role; the
//     framework calls AllowMeshConflict with (current, candidate,
//     Conflict{Role: <RoleAuth|RoleAuthz>}) and uses the result
//     verbatim.
//   - Federation preemption: name already federated by a different
//     address; the framework calls AllowMeshConflict with
//     (current, candidate, Conflict{FederatedAddrs: [old, new]}) —
//     "current" is the wire name, "candidate" is the same wire name,
//     and the address pair is in FederatedAddrs.
//
// Framework iterates registered policies; first true wins. Default
// (no policy registered, or all return false) is deny —
// handleRegister returns 409 ROLE_CONFLICT / SERVICE_CONFLICT.
//
// ConsumeConflict fires AFTER the takeover succeeds so the plugin can
// drop the rule it owns (one-shot preemption map cleanup, audit log).
// Plugins that have nothing to clean up should leave it a no-op.
//
// Replaces the prior RoleConflictPolicy + FederationPreemptionPolicy
// pair — both folded under one name with Conflict carrying the case
// discriminator.
type MeshConflictPolicy interface {
	AllowMeshConflict(current, candidate string, c Conflict) bool
	ConsumeConflict(name string, c Conflict)
}

// Conflict carries the case discriminator for MeshConflictPolicy.
// Exactly one of Role / FederatedAddrs is populated at any call site:
//   - Role != 0 means role-takeover (FederatedAddrs is zero strings).
//   - FederatedAddrs[0] != "" means federation preemption
//     (Role is zero); FederatedAddrs is [oldAddr, newAddr].
type Conflict struct {
	Role           RoleFlag  // RoleAuth | RoleAuthz, zero if federation
	FederatedAddrs [2]string // [old, new] for federation case; zero strings if role case
}

// ---------------------------------------------------------------------------
// Trust-hook cluster — two independent interfaces that both gate inbound
// X-Sov-* claim bundles when TrustUpstreamClaims is enabled. Grouped here
// for documentation; the framework type-asserts on each inner name
// individually so a plugin implements only the subset it needs:
//   - TrustUpstream: vet by upstream URL allowlist (UpstreamTrustPolicy)
//   - VerifySeal:    vet by HMAC seal verification (SealVerifier)
// ---------------------------------------------------------------------------

// UpstreamTrustPolicy decides whether inbound X-Sov-* claim headers
// from the given request should be trusted. Trust guard iterates
// registered policies; ALL must return true (logical AND) for the
// bundle to pass. If NO UpstreamTrustPolicy is registered, the
// allowlist check is skipped (a request passes the upstream gate
// trivially) — pair with a SealVerifier to keep the trust guard
// honest.
type UpstreamTrustPolicy interface {
	TrustUpstream(headers map[string][]string) bool
}

// SealVerifier decides whether inbound X-Sov-* claim headers carry a
// valid HMAC seal under any registered key. Trust guard iterates
// registered verifiers; first true wins. If NO SealVerifier is
// registered AND TrustUpstreamClaims is true, the seal check is
// skipped — pair with an UpstreamTrustPolicy in that case.
type SealVerifier interface {
	VerifySeal(headers map[string][]string) bool
}

// RecoveryHandler observes every hook failure (returned error OR
// recovered panic). Plugins implement it to log, sample, alert, or
// shape the 500-response. Framework installs a default stderr-logging
// handler when no plugin registers one.
//
// The handler may return a non-nil response override; the framework uses
// it instead of the default 500 envelope. Return nil to accept the
// default. A plugin-supplied RespondErr (see plugin_errors.go) always
// wins over a handler override.
type RecoveryHandler interface {
	HandleHookFailure(failure HookFailure) (override *Response)
}

// LogLevel is the level passed to Logger.Log. Standard four — Debug,
// Info, Warn, Error — covering 95% of consumer needs without forcing
// a specific log lib's enum. Plugins implementing Logger map these to
// whatever level convention they use.
type LogLevel string

const (
	LogDebug LogLevel = "debug"
	LogInfo  LogLevel = "info"
	LogWarn  LogLevel = "warn"
	LogError LogLevel = "error"
)

// Logger is the structured logging contract. Plugins that satisfy
// this become the gateway-wide logger sink — framework + every
// builtin route Log() calls through it.
//
// Signature is slog-compatible (Debug/Info/Warn/Error all take
// `(msg, args...)` — args are key-value pairs). A *slog.Logger
// satisfies Logger directly. Other libs (zap, logrus, custom) need a
// tiny adapter (4 methods).
//
// First registered Logger wins. When none, gw.Log() returns a slog
// adapter backed by slog.Default() so log lines still land
// somewhere.
type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// Capability is a plugin-published, type-safe contract another plugin
// (or business handler) consumes. Plugin advertises capabilities via
// CapabilityProvider; consumers look them up with GetCapability[T]
// (see capability.go). Convention: Type uses "<plugin>.<contract>"
// namespace (e.g. "requestid.IDGenerator", "audit.Recent") so the
// origin is visible at a glance and collisions are rare.
//
//	type IDGenerator func() string
//
//	func (p *Plugin) Capabilities() []gateway.Capability {
//	    return []gateway.Capability{
//	        {Type: "requestid.IDGenerator", Impl: IDGenerator(p.generate)},
//	    }
//	}
type Capability struct {
	Type string
	Impl any
}

// CapabilityProvider opts a plugin into capability publication.
// gw.Use() harvests Capabilities() once at registration. Multiple
// plugins MAY publish the same Type — first wins on lookup, but the
// framework records all and surfaces them in introspect for drift
// detection.
type CapabilityProvider interface {
	Capabilities() []Capability
}

// PluginDependency declares cross-plugin ordering constraints.
// gw.Use errors out when any Requires() name isn't yet registered —
// fail-fast on misconfiguration. After() is a soft, diagnostic hint
// that the plugin documents its preferred ordering; the framework
// records it for introspect surfacing + tooling, but does NOT
// reorder calls automatically (operator-controlled Use order is
// load-bearing for predictability).
//
//	func (p *CorsPlugin) Requires() []string { return []string{"request-id"} }
//	func (p *CorsPlugin) After() []string    { return []string{"audit"} }
//
// Wire cors after request-id or gw.Use returns:
//
//	gw.Use: cors requires plugin "request-id" which is not registered
type PluginDependency interface {
	Requires() []string
	After() []string
}

// ResponseInterceptor fires AFTER dispatch with the resolved
// *Response. Plugin may mutate Status, Header, Body — or replace
// them entirely. Runs in registration order so each interceptor sees
// the output of the previous one.
//
// Use cases: CORS header injection on every response, response
// compression, body redaction, status remapping, structured logging
// of the final envelope.
//
// Fires for ALL responses: framework endpoints, plugin routes,
// business dispatch. The runtime tags the Response.Mode field before
// this hook fires so interceptors can branch by dispatch source.
type ResponseInterceptor interface {
	InterceptResponse(req *Request, resp *Response) error
}

// ContextContributor lets a plugin stash per-request metadata onto
// the *rpc.Context that the in-process engine hands to local
// handlers. Fires in dispatchLocal AFTER the framework's own
// stashes (Authorization, Claims, path) but BEFORE engine.Dispatch.
//
// The symmetric counterpart of HeaderInjector for the local path —
// HeaderInjector adds bytes to outbound HTTP requests; this adds
// values to in-process ctx. A plugin that wants its metadata
// available in BOTH paths (request-id, trace-id, tenant) implements
// both. Plugin owns the ctx-key namespace; conventionally
// "sov.<plugin>.<field>".
//
//	func (p *Plugin) ContributeContext(ctx *rpc.Context, req *gateway.Request) {
//	    ctx.Set("sov.requestid", req.Header.Get("X-Sov-Request-Id"))
//	}
type ContextContributor interface {
	ContributeContext(ctx *rpc.Context, req *Request) error
}

// HealthAggregator lets a plugin merge remote-pod health probes into
// the framework's local /rpc/_health report. Framework calls every
// registered aggregator AFTER building the local report (gateway +
// in-process services) and BEFORE marshalling. Plugin mutates
// *HealthReport directly — services map may be extended; the
// top-level Status field should be downgraded ONLY if a probed
// remote returned degraded/unhealthy.
type HealthAggregator interface {
	AggregateHealth(ctx context.Context, report *HealthReport) error
}

// RouteHandler lets a plugin own a path on the gateway, instead of
// wrapping the dispatch chain in a Middlewarer just to peek for a
// prefix. Patterns follow net/http ServeMux convention: a trailing
// "/" matches the subtree (prefix match); no trailing "/" matches the
// exact path only. Routes are checked AFTER the framework's own
// endpoints (_health/_introspect/_register/_batch) so plugins extend
// the framework surface but cannot shadow built-ins. Match order is
// registration order — register the most specific patterns first.
type RouteHandler interface {
	RoutePatterns() []string
	ServeRoute(ctx context.Context, req *Request) *Response
}

// PluginInfo is the introspect-time descriptor of a registered plugin.
// Surfaces in IntrospectReport.Plugins; the explorer's Plugins tab
// renders it; drift radar uses it to flag same-name-different-version
// across team gateways.
type PluginInfo struct {
	Name         string         `json:"name"`
	Hooks        []string       `json:"hooks"`                  // ["HeaderInjector", "DispatchHook"]
	HasRouter    bool           `json:"has_router"`             // true when the plugin also registered RPC methods
	Requires     []string       `json:"requires,omitempty"`     // hard deps from PluginDependency
	After        []string       `json:"after,omitempty"`        // soft ordering hint from PluginDependency
	Capabilities []string       `json:"capabilities,omitempty"` // Type names published via CapabilityProvider
	Extra        map[string]any `json:"extra,omitempty"`        // per-plugin metadata (version, source URL, ...)
}
