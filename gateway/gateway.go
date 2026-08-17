package gateway

import (
	"context"
	"fmt"
	"net/http"
	"os/signal"
	"reflect"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Toyz/sov/rpc"
)

// Gateway is the entry point. It wraps a rpc.Engine (services
// in-process) and a Resolver chain (services elsewhere) behind a
// pluggable Server. The same Gateway can do both — that is the PEMM
// thesis the framework promises.
type Gateway struct {
	engine        *rpc.Engine
	resolver      Resolver
	resolverChain *dynamicChain // same as resolver, typed for plugin appends
	server        Server
	register      *RegisterResolver
	proxy         *http.Client
	breakers      *breakerManager // per-upstream circuit breaker on remote dispatch
	serving       atomic.Int32    // serving state (starting|ready|draining) for /rpc/_ready
	inFlight      atomic.Int64    // requests currently in the dispatch chain (in-flight gauge)
	maxInFlight   int64           // load-shed cap; 0 = unlimited
	retry         RetryConfig     // bounded remote-dispatch retry (W1.4); off unless MaxAttempts>1
	dispatch      Handler         // wrapped chain (middleware → handle)

	// Auth + authz bindings. Both optional. The gateway calls the
	// designated service via its own dispatch path for every request
	// (with caching on the auth side).
	authBinding  *AuthBinding
	authzBinding *AuthzBinding
	authCache    ClaimsCache

	// trustUpstreamWired records whether the trust guard is wired on
	// the underlying server. The BootValidator at ListenAndServe time
	// uses this to enforce the "trust guard needs at least one proof
	// plugin" invariant.
	trustUpstreamWired bool
	advertiseURL       string // stamped as X-Sov-Upstream on outbound proxy hops (Options.AdvertiseURL)

	// catalog caches the aggregated _introspect body for catalogTTL
	// (30s). Invalidated by RegisterResolver register/expire events
	// via its onChange hook.
	catalog catalogCache

	// introspectExposed gates the PUBLIC /rpc/_introspect endpoint. It is
	// OFF by default — the catalog discloses the full service/method/type
	// surface, so the endpoint is opt-in via gw.Use(introspect.New())
	// (which calls ExposeIntrospect). The report itself is always buildable
	// in-process via IntrospectBody — the explorer and federation use that
	// directly, so they keep working without the public endpoint being open.
	introspectExposed bool

	// configExposed gates the opt-in /rpc/_config runtime-config dump. OFF by
	// default — the report is sanitized (no secrets) but still discloses
	// operational topology, so it is opened only via gw.Use(configdump.New()).
	configExposed bool

	// muMiddleware guards dynamic Use() appends after construction.
	muMiddleware sync.Mutex
	middlewares  []Middleware
	innerHandler Handler

	// muPlugins guards g.plugins from concurrent Use(). The dispatch
	// fan-out paths (plugin_hooks.go) snapshot g.plugins under RLock and
	// filter by sub-interface — there are no per-hook slot lists.
	muPlugins       sync.RWMutex
	plugins         []*pluginEntry
	pluginRoutes    []pluginRoute
	defaultRecovery *defaultRecoveryHandler
	// hookCache memoizes PluginsImplementing[T] results by interface type so the
	// dispatch fan-outs don't re-type-assert every plugin per request. Guarded by
	// muPlugins; cleared whenever a plugin is added. nil until first use.
	hookCache map[reflect.Type][]any

	muStart sync.Mutex
}

// Options configures a Gateway. All fields optional — defaults are
// sensible for the standalone-single-binary case.
type Options struct {
	Server              Server            // default NewNetHTTPServer
	Resolver            Resolver          // default chain: Local + RegisterResolver
	RegisterResolver    *RegisterResolver // default NewRegisterResolver(5s)
	ProxyClient         *http.Client      // default http.Client{Timeout: 30s}
	Middleware          []Middleware      // appended after the gateway's own auth/authz middleware
	TrustUpstreamClaims bool              // when true, the default NetHTTPServer trusts inbound X-Sov-*
	// AdvertiseURL, when set, makes the framework stamp X-Sov-Upstream
	// on every outbound proxy hop so downstream pods running the
	// upstreams plugin can verify the claim bundle came from a trusted
	// peer. Empty disables the stamp. URL is normalized at New().
	AdvertiseURL string
	// ClaimsCache overrides the in-memory verified-claims cache. Supply a
	// Redis/memcached-backed impl to share auth-verify results across a
	// fleet of gateway replicas. Default: per-replica in-memory.
	ClaimsCache ClaimsCache
	// AuthCacheTTL caps how long a verified bearer is trusted from the DEFAULT
	// cache before re-verifying against the auth service — the bound on
	// revocation lag (and the only bound for a token with no expiry). Default
	// 5m when 0. Ignored when a custom ClaimsCache is supplied (that impl owns
	// its own TTL). Set very large to effectively disable the cap.
	AuthCacheTTL time.Duration
	// RemoteBreaker tunes the per-upstream circuit breaker on remote
	// dispatch (default: 5 consecutive failures → open, 10s cooldown). Set
	// RemoteBreaker.Disabled to turn it off.
	RemoteBreaker BreakerConfig
	// MaxInFlight load-sheds: when more than this many requests are being
	// handled concurrently, further requests get an immediate retryable 503
	// OVERLOADED instead of being accepted and exhausting goroutines/FDs.
	// 0 (default) = unlimited.
	MaxInFlight int
	// Retry bounds automatic retry of a failed REMOTE dispatch (W1.4). Off by
	// default; see RetryConfig + WithRetries.
	Retry RetryConfig
}

// RetryConfig bounds automatic retry of a failed REMOTE dispatch (W1.4). Retry
// is OFF by default (MaxAttempts <= 1); enable it with WithRetries. A retry
// re-resolves onto a DIFFERENT replica (round-robin; open breakers skipped).
// Only a provably-never-executed failure (breaker open / dial refused) is
// retried unconditionally; an ambiguous failure (timeout) or an upstream 5xx is
// retried ONLY when the request carries an Idempotency-Key, so a non-idempotent
// op is never silently re-sent. Backoff is exponential with full jitter and
// never sleeps past the request deadline (W1.2).
type RetryConfig struct {
	MaxAttempts int           // total attempts including the first; <=1 disables retry
	BaseBackoff time.Duration // first-retry backoff; default 20ms
	MaxBackoff  time.Duration // backoff cap; default 1s
}

// normalizeRetry fills defaults; MaxAttempts<=1 leaves retry disabled.
func normalizeRetry(c RetryConfig) RetryConfig {
	if c.MaxAttempts < 1 {
		c.MaxAttempts = 1
	}
	if c.BaseBackoff <= 0 {
		c.BaseBackoff = 20 * time.Millisecond
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = time.Second
	}
	return c
}

// Middleware wraps the gateway's request handler. Return a non-nil
// Response to short-circuit dispatch; return nil to fall through to the
// next middleware (or the gateway's own handler).
type Middleware func(next Handler) Handler

// Handler is the gateway's per-request callable. Middleware wrap it.
type Handler func(ctx context.Context, req *Request) *Response

// Option mutates Options.
type Option func(*Options)

// WithServer overrides the HTTP server (default: NewNetHTTPServer).
func WithServer(s Server) Option { return func(o *Options) { o.Server = s } }

// WithResolver sets the resolver chain. Default: a chain of LocalResolver + RegisterResolver.
func WithResolver(r Resolver) Option { return func(o *Options) { o.Resolver = r } }

// WithRegisterResolver sets the resolver backing /_register. Defaults to
// NewRegisterResolver(5s) — pass one explicitly if you need Close() or
// custom eviction.
func WithRegisterResolver(r *RegisterResolver) Option {
	return func(o *Options) { o.RegisterResolver = r }
}

// WithProxyClient overrides the http.Client used to proxy remote calls.
func WithProxyClient(c *http.Client) Option { return func(o *Options) { o.ProxyClient = c } }

// WithClaimsCache overrides the verified-claims cache (default: in-memory,
// per-replica). Pass a shared (e.g. Redis) implementation so gateway
// replicas reuse each other's AuthService.verify results. See ClaimsCache.
func WithClaimsCache(c ClaimsCache) Option { return func(o *Options) { o.ClaimsCache = c } }

// WithTrustUpstreamClaims is a top-level lift of NetHTTPOptions.
// TrustUpstreamClaims so consumers don't have to construct a server by
// hand. Pods set this true (the upstream gateway is trusted to inject
// X-Sov-*); edge gateways leave it false (strip inbound X-Sov-*).
func WithTrustUpstreamClaims(b bool) Option {
	return func(o *Options) { o.TrustUpstreamClaims = b }
}

// WithAdvertiseURL sets the public address this gateway stamps as
// X-Sov-Upstream on every outbound proxy hop. Replaces the
// builtin/advertise plugin (folded into core).
func WithAdvertiseURL(url string) Option {
	return func(o *Options) { o.AdvertiseURL = url }
}

// WithMiddleware appends middleware that wrap the dispatch. Consumer
// middleware runs AFTER the gateway's own auth + authz middleware (when
// configured) so it can rely on Claims being resolved.
func WithMiddleware(mw ...Middleware) Option {
	return func(o *Options) { o.Middleware = append(o.Middleware, mw...) }
}

// WithRemoteBreaker tunes the per-upstream circuit breaker on remote dispatch.
// See Options.RemoteBreaker.
func WithRemoteBreaker(cfg BreakerConfig) Option {
	return func(o *Options) { o.RemoteBreaker = cfg }
}

// WithAuthCacheTTL caps how long a verified bearer is trusted from the default
// claims cache before re-verifying — the revocation-lag bound. See
// Options.AuthCacheTTL.
func WithAuthCacheTTL(d time.Duration) Option {
	return func(o *Options) { o.AuthCacheTTL = d }
}

// WithMaxInFlight load-sheds: concurrent requests above n get a retryable 503
// OVERLOADED. 0 = unlimited. See Options.MaxInFlight.
func WithMaxInFlight(n int) Option {
	return func(o *Options) { o.MaxInFlight = n }
}

// WithRetries enables bounded automatic retry of failed remote dispatches
// (W1.4). See RetryConfig. cfg.MaxAttempts <= 1 leaves retry off.
func WithRetries(cfg RetryConfig) Option {
	return func(o *Options) { o.Retry = cfg }
}

// New constructs a Gateway. All Options are optional — the bare
// gateway.New() returns a usable standalone gateway with sensible
// defaults. The gateway always owns its rpc.Engine internally; reach
// it via g.Engine() only for power-user escape hatches.
//
// New panics on an Option carrying config it cannot honor — e.g.
// WithAdvertiseURL with an unparseable URL — so a misconfigured gateway
// fails loudly at construction rather than silently mis-routing later.
func New(opts ...Option) *Gateway {
	o := &Options{}
	for _, fn := range opts {
		fn(o)
	}
	if o.Server == nil {
		o.Server = NewNetHTTPServer(NetHTTPOptions{TrustUpstreamClaims: o.TrustUpstreamClaims})
	}
	if o.RegisterResolver == nil {
		o.RegisterResolver = NewRegisterResolver(5 * time.Second)
	}
	eng := rpc.NewEngine()
	// resolver is always a dynamicChain so gw.Use(resolverPlugin) can
	// append after construction without rebuilding state. Base links
	// come from Options.Resolver when set, otherwise the standard
	// [local, register] pair.
	var baseLinks []Resolver
	if o.Resolver != nil {
		baseLinks = []Resolver{o.Resolver}
	} else {
		baseLinks = []Resolver{
			NewLocalResolverFunc(eng.HasRouter, eng.Routers),
			o.RegisterResolver,
		}
	}
	dchain := newDynamicChain(baseLinks...)
	if o.ProxyClient == nil {
		o.ProxyClient = &http.Client{Timeout: 30 * time.Second}
	}
	if o.ClaimsCache == nil {
		ttl := o.AuthCacheTTL
		if ttl == 0 {
			ttl = 5 * time.Minute
		}
		o.ClaimsCache = newMemClaimsCache(ttl)
	}
	g := &Gateway{
		engine:        eng,
		resolver:      dchain,
		resolverChain: dchain,
		server:        o.Server,
		register:      o.RegisterResolver,
		proxy:         o.ProxyClient,
		authCache:     o.ClaimsCache,
		middlewares:   append([]Middleware{}, o.Middleware...),
		breakers:      newBreakerManager(o.RemoteBreaker),
		maxInFlight:   int64(o.MaxInFlight),
		retry:         normalizeRetry(o.Retry),
	}
	g.defaultRecovery = &defaultRecoveryHandler{gw: g}
	// Route rpc.Engine boot warnings (HELL-281) through the gateway's
	// structured logger instead of the stdlib log package. Resolved
	// dynamically at warn-time, so a Logger plugin registered later is
	// honored.
	eng.SetLogger(engineLogger{g: g})
	g.register.onChange = g.invalidateCatalog
	// Let the resolver skip replicas whose breaker is open when picking one
	// (W1.1). Non-mutating read; the dispatch path still owns the half-open
	// probe. Safe even when the breaker is disabled — isOpen returns false.
	g.register.breakerOpen = g.breakers.isOpen
	if o.AdvertiseURL != "" {
		canon, err := NormalizeUpstreamURL(o.AdvertiseURL)
		if err != nil {
			panic("gateway.New: invalid AdvertiseURL: " + err.Error())
		}
		g.advertiseURL = canon
	}
	// Wire trust guard onto the server when it's the default NetHTTPServer.
	// Custom Server implementations can set their own equivalent. The
	// guard iterates registered UpstreamTrustPolicy + SealVerifier
	// plugins at REQUEST time (not at construction), so plugins
	// registered after New() take effect on the next request.
	if ns, ok := o.Server.(*NetHTTPServer); ok && o.TrustUpstreamClaims {
		g.trustUpstreamWired = true
		ns.SetTrustGuard(func(r *http.Request, body []byte) bool {
			if !g.upstreamTrusted(r.Header) {
				return false
			}
			if !g.sealValid(r.Header) {
				return false
			}
			return true
		})
	}
	// HeaderClaim predicate — always wired so plugins can claim
	// identity-shaped headers regardless of trust mode.
	if ns, ok := o.Server.(*NetHTTPServer); ok {
		ns.SetHeaderClaim(g.headerClaimed)
	}
	// Middleware chain (outer-first):
	//   [authMiddleware] → [authzMiddleware] → [consumer middleware...] → handle
	// Auth + authz are wired unconditionally — they no-op when the
	// gateway has no binding, but installing them up-front means a
	// runtime _register call that binds auth (e.g. AuthService starts
	// up after the gateway) takes effect immediately.
	g.innerHandler = Handler(g.handle)
	g.rebuildChain()
	o.Server.Handle(func(ctx context.Context, req *Request) *Response {
		return g.dispatch(ctx, req)
	})
	return g
}

// rebuildChain composes [auth, authz, consumer middlewares...] around
// g.innerHandler. Called at construction and again on every Use() so
// dynamically-appended middleware takes effect immediately.
func (g *Gateway) rebuildChain() {
	g.muMiddleware.Lock()
	defer g.muMiddleware.Unlock()
	all := []Middleware{g.recordDispatchMiddleware(), g.authMiddleware(), g.authzMiddleware()}
	all = append(all, g.middlewares...)
	chain := g.innerHandler
	for i := len(all) - 1; i >= 0; i-- {
		chain = all[i](chain)
	}
	g.dispatch = chain
}

// UseMiddleware appends a raw Middleware closure to the dispatch
// chain. The plugin-shaped `Use(any)` is preferred for new code; this
// is the back-compat path for the closure form. May be called before
// or after ListenAndServe.
func (g *Gateway) UseMiddleware(mw Middleware) {
	g.muMiddleware.Lock()
	g.middlewares = append(g.middlewares, mw)
	g.muMiddleware.Unlock()
	g.rebuildChain()
}

// Resolver returns the resolver chain so the registry plugin can
// check whether a wire name is already federated by another address.
func (g *Gateway) Resolver() Resolver { return g.resolver }

// ProxyClient returns the http client used for outbound proxy hops.
// Registry plugin uses this for the introspect/health fan-out so the
// timeouts and TLS config stay consistent with business calls.
func (g *Gateway) ProxyClient() *http.Client { return g.proxy }

// InvalidateCatalog drops the cached aggregated _introspect body so
// the next probe rebuilds. Registry plugin calls this after a
// /rpc/_register write.
func (g *Gateway) InvalidateCatalog() { g.invalidateCatalog() }

// PolicyAllowsRoleTakeover exposes the mesh-conflict iterator for the
// role-takeover path. Registry plugin calls this when a different
// service name tries to claim an auth/authz role another service
// already holds. Defers to registered MeshConflictPolicy plugins.
func (g *Gateway) PolicyAllowsRoleTakeover(current, candidate string, role RoleFlag) bool {
	return g.policyAllowsMeshConflict(current, candidate, Conflict{Role: role})
}

// PreemptFederation exposes the mesh-conflict iterator for the
// federation-preemption path. Registry plugin calls this when a
// federated _register tries to claim a wire name already federated by
// a different address. Defers to registered MeshConflictPolicy
// plugins.
func (g *Gateway) PreemptFederation(svc, oldAddr, newAddr string) bool {
	return g.policyAllowsMeshConflict(svc, svc, Conflict{FederatedAddrs: [2]string{oldAddr, newAddr}})
}

// ConsumeFederationPreemption notifies registered MeshConflictPolicy
// plugins that a preempted federation rule has been consumed (the
// takeover succeeded). Plugins drop the rule they own.
func (g *Gateway) ConsumeFederationPreemption(svc string) {
	g.consumeMeshConflict(svc, Conflict{FederatedAddrs: [2]string{"", ""}})
}

// Engine returns the underlying rpc.Engine — an escape hatch for power
// users. Normal consumers register via g.Register / g.RegisterRemote
// and never need this.
func (g *Gateway) Engine() *rpc.Engine { return g.engine }

// Register adds an in-process router to the gateway's engine. If the
// router happens to satisfy AuthService or AuthzService, the gateway
// auto-binds the corresponding role — same effect as RegisterAuth /
// RegisterAuthz but without the consumer having to know which routers
// hold which roles. Mesh pods self-declare via Roles on JoinMesh;
// monolith mode discovers the role by interface implementation. Both
// paths land in bindAuth / bindAuthz.
//
// Boot-panics if two distinct routers implement the same role
// interface — see bindAuth / bindAuthz for the message.
func (g *Gateway) Register(router any) {
	g.engine.Register(router)
	g.autoBindRoles(router)
	// A router registered after the catalog warmed must show up in
	// introspection-driven surfaces (MCP tools/list, explorer) immediately,
	// not after the 30s TTL — same invalidation a remote join/leave triggers.
	g.invalidateCatalog()
}

// RegisterResolver returns the gateway's register-based remote resolver
// so callers can pre-populate entries or Close on shutdown.
func (g *Gateway) RegisterResolver() *RegisterResolver { return g.register }

// Handle dispatches one Request through the full middleware chain and
// returns the resulting Response. Useful for tests and for embedding
// the gateway inside a custom Server implementation that wants to call
// dispatch directly rather than going through Server.Handle.
func (g *Gateway) Handle(ctx context.Context, req *Request) *Response {
	return g.dispatch(ctx, req)
}

// Run is the ergonomic main() helper: installs SIGINT/SIGTERM
// handling that cancels the supplied ctx, then calls ListenAndServe.
// On signal, the gateway's LifecycleHook.OnStop fires and the server
// gracefully drains in-flight requests. Returns nil on clean
// shutdown, error on boot/serve failure.
//
//	func main() {
//	    log.Fatal(sov.NewMonolith(cfg).Run(context.Background(), ":8080"))
//	}
func (g *Gateway) Run(ctx context.Context, addr string) error {
	return g.runWithSignals(ctx, func(ctx context.Context) error {
		return g.ListenAndServe(ctx, addr)
	})
}

// runWithSignals installs SIGINT/SIGTERM handling that cancels ctx, runs
// the blocking fn, and treats context.Canceled (clean shutdown via signal)
// as success. Shared by Run and RunMesh.
func (g *Gateway) runWithSignals(ctx context.Context, fn func(context.Context) error) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := fn(ctx); err != nil && err != context.Canceled {
		return err
	}
	return nil
}

// ListenAndServe starts the underlying server. Returns when ctx is
// cancelled or the server returns an error.
//
// Plugin lifecycle: every registered BootValidator runs first; the
// first error refuses startup. Then every LifecycleHook.OnStart runs.
// On context cancel or server return, LifecycleHook.OnStop runs in
// reverse registration order.
func (g *Gateway) ListenAndServe(ctx context.Context, addr string) error {
	g.muStart.Lock()
	defer g.muStart.Unlock()
	defer g.register.Close()
	// Inter-service identity propagation is trust-by-default: a pod that
	// opts into WithTrustUpstreamClaims(true) accepts X-Sov-* from its
	// gateway with no per-request crypto. That's the right model for a
	// network-isolated mesh (the common case) and is what keeps monolith,
	// hybrid, and mesh behaving identically with zero seal wiring.
	//
	// Per-request cryptographic proof (SealVerifier / UpstreamTrustPolicy)
	// is OPT-IN hardening for zero-trust networks — add a verifier plugin
	// when the network between gateway and pod is not trusted. We warn
	// (not refuse) when trust is on with no verifier so the operator knows
	// the pod is relying on network isolation to keep X-Sov-Subject honest.
	if g.trustUpstreamWired && !g.hasSealVerifier() {
		g.Log().Warn("gateway: trusting inbound X-Sov-* claims with no SealVerifier — identity is only as safe as the network. " +
			"An upstream-allowlist filter (the upstreams plugin) is NOT cryptographic proof: it matches a client-supplied X-Sov-Upstream header and cannot stop a caller who reaches the pod directly from forging X-Sov-Subject. " +
			"Add gw.Use(hmacseal.New(...)) keyed to your mesh secret to require cryptographic proof on untrusted networks.")
	}
	if err := g.reorderPluginsByDependency(); err != nil {
		return err
	}
	// Surface what each plugin actually bound (post-ordering) so a hook
	// that silently failed to bind — a duck-typed signature drift — is
	// visible at boot instead of mysteriously never firing. Debug-level:
	// flip on the logger to debug "why didn't my hook run".
	g.logPluginBindings()
	if err := g.callBootValidators(); err != nil {
		return fmt.Errorf("gateway: boot validation failed: %w", err)
	}
	if err := g.callLifecycleStart(ctx); err != nil {
		return fmt.Errorf("gateway: lifecycle start failed: %w", err)
	}
	// Now serving: /rpc/_ready flips to 200. On ctx cancel it flips to draining
	// (503) BEFORE the server's graceful drain runs, so an orchestrator stops
	// routing NEW traffic here while in-flight requests finish.
	g.serving.Store(servingReady)
	go func() {
		<-ctx.Done()
		g.serving.Store(servingDraining)
	}()
	defer g.callLifecycleStop(context.Background())
	return g.server.ListenAndServe(ctx, addr)
}
