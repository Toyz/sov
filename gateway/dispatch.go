package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/Toyz/sov/rpc"
)

// handle is the single RequestHandler the Server invokes per request.
// Order of operations:
//  1. Framework endpoints (/rpc/_health, /rpc/_introspect, /rpc/_batch,
//     /rpc/_register) — dispatched against gateway-owned handlers.
//  2. Plugin RouteHandlers, most-specific first (handleInner). Surface builtins
//     live here: the rpc builtin owns /rpc/{router}/{method} and enforces its
//     own POST-only + reserved-`_`-name policy before handing off to Dispatch;
//     mcp owns /mcp. A gateway with no surface registered 404s business paths.
func (g *Gateway) handle(ctx context.Context, req *Request) *Response {
	// Plugin hook: HeaderParser runs on every inbound request before
	// any routing decision. A parser may short-circuit by returning an
	// error; the typical use is stashing values onto req.Header or
	// req.User without erroring.
	started := time.Now()
	// Capture the pre-parser header state so sov:"header=" params bind the same
	// values the authz gate saw — a HeaderParser must not silently change what a
	// header-bound param resolves to. Only when a registered method uses header=.
	if g.engine.NeedsHeaderGetter() {
		req.headerSnapshot = req.Header.Clone()
	}
	var resp *Response
	if perr := g.callHeaderParsers(req); perr != nil {
		resp = ErrorResponse(perr)
	} else {
		resp = g.handleInner(ctx, req)
	}
	// Plugin hook: ResponseInterceptor fires post-dispatch with the
	// mutable *Response. Plugins (cors, compression, status remap)
	// modify Status/Header/Body. Runs BEFORE DispatchHook so the
	// recorded status reflects the final post-intercept value.
	g.callResponseInterceptors(req, resp)
	// Plugin hook: DispatchHook fires post-handler with the resolved
	// router/method/status. Framework endpoints get an empty
	// router/method so hooks can filter by Path.
	// Skip the generic per-request event if a surface already recorded a
	// specific one for this request (RecordDispatch, e.g. MCP tools/call).
	if !req.recorded {
		router, method, _ := rpc.SplitRPCPath(req.Path)
		g.recordDispatchEventWithMode(router, method, req.Path, resp.Status, started, subjectOf(req), errCodeFromBody(resp.Body), "", resp.Mode, req.RemoteIP, requestIDOf(req, resp))
		req.recorded = true
	}
	return resp
}

// deadlineHeader carries the caller's absolute deadline (unix nanoseconds) so a
// downstream hop can bound its own work to the remaining budget instead of each
// hop independently waiting out a full timeout. The deadline builtin honors it
// on ingress; here we only propagate it.
const deadlineHeader = "X-Sov-Deadline"

// stampDeadline propagates the ctx deadline onto an outbound hop. No-op when the
// context has no deadline, so it costs nothing unless a deadline is in play.
func stampDeadline(ctx context.Context, hreq *http.Request) {
	if dl, ok := ctx.Deadline(); ok {
		hreq.Header.Set(deadlineHeader, strconv.FormatInt(dl.UnixNano(), 10))
	}
}

// remoteIPOf returns req.RemoteIP, nil-safe.
func remoteIPOf(req *Request) string {
	if req == nil {
		return ""
	}
	return req.RemoteIP
}

// requestIDHeader is the canonical correlation-id header (owned on the wire by
// the requestid builtin). Kept here so the recorder can stamp the id onto
// DispatchEvent without importing the plugin.
const requestIDHeader = "X-Sov-Request-Id"

// requestIDOf returns the correlation id for the event — the id the requestid
// builtin stamped on the response, falling back to an upstream-supplied inbound
// one, else empty. Nil-safe.
func requestIDOf(req *Request, resp *Response) string {
	if resp != nil {
		if id := resp.Header.Get(requestIDHeader); id != "" {
			return id
		}
	}
	if req != nil {
		return req.Header.Get(requestIDHeader)
	}
	return ""
}

// recordDispatchMiddleware is the OUTERMOST middleware. It exists so a request
// rejected by the auth or authz middleware (401/403) — which short-circuits
// before handle() runs and thus never reaches handle's own recording — is still
// emitted as a DispatchEvent, so audit + metrics see auth failures and authz
// denials, not just calls that made it to a handler. handle sets req.recorded
// when it records a call that got through, so this fires only for the
// pre-handler short-circuits it would otherwise miss.
func (g *Gateway) recordDispatchMiddleware() Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, req *Request) *Response {
			started := time.Now()
			g.inFlight.Add(1)
			defer g.inFlight.Add(-1)
			resp := next(ctx, req)
			if resp != nil && !req.recorded {
				router, method, _ := rpc.SplitRPCPath(req.Path)
				g.recordDispatchEventWithMode(router, method, req.Path, resp.Status, started, subjectOf(req), errCodeFromBody(resp.Body), "", resp.Mode, req.RemoteIP, requestIDOf(req, resp))
				req.recorded = true
			}
			return resp
		}
	}
}

func (g *Gateway) handleInner(ctx context.Context, req *Request) *Response {
	if resp := g.frameworkEndpoint(ctx, req); resp != nil {
		if resp.Mode == "" {
			resp.Mode = ModeFramework
		}
		return resp
	}
	// Plugin-owned routes via RouteHandler, tried MOST-SPECIFIC first. Framework
	// endpoints (_health, _introspect, _batch, _register) were already handled
	// above, so a plugin cannot shadow them. A handler returning nil DECLINES;
	// routing then falls through to the next-broadest match. This is how the /rpc
	// surface builtin ("/rpc/") coexists with a more-specific /rpc/_explorer/
	// plugin and a catch-all "/" SPA. Common case (a single winning match) is a
	// zero-alloc single scan; the slice is built only on a decline (rare).
	g.muPlugins.RLock()
	snap := g.pluginRoutes
	g.muPlugins.RUnlock()
	if best := longestPluginRoute(snap, req.Path); best >= 0 {
		if resp := g.runPluginRoute(snap[best], ctx, req); resp != nil {
			return resp
		}
		for _, r := range pluginRoutesExcept(snap, req.Path, best) {
			if resp := g.runPluginRoute(r, ctx, req); resp != nil {
				return resp
			}
		}
	}
	// No surface claimed it. A gateway with no rpc builtin (gw.Use(rpc.New()))
	// simply doesn't speak the /rpc surface — the Dispatch fabric still serves
	// other surfaces (MCP) over the same registered routers.
	return ErrorResponse(rpc.NotFound("no surface for %q — register a surface plugin (e.g. rpc.New())", req.Path))
}

// subjectOf returns the authenticated subject the auth middleware stamped onto
// req (a subject string or *Claims), or "" for an anonymous request.
func subjectOf(req *Request) string {
	if req == nil {
		return ""
	}
	switch u := req.User.(type) {
	case string:
		return u
	case *Claims:
		if u != nil {
			return u.Subject
		}
	}
	return ""
}

// PreParserHeader returns the header state captured at gateway ingress — before
// any HeaderParser plugin mutated req.Header — falling back to req.Header when no
// snapshot was taken (no registered method uses header= params). A surface that
// AUTHORIZES or BINDS header= params off an inbound request should use this so
// its view matches the /rpc surface, which authorizes and binds pre-parser. See
// docs/HEADER_PARAMS.md.
func (g *Gateway) PreParserHeader(req *Request) Header {
	if req == nil {
		return nil
	}
	if req.headerSnapshot != nil {
		return req.headerSnapshot
	}
	return req.Header
}

// InheritRequestSnapshot copies the pre-parser header snapshot from parent onto
// a synthetic sub-request, so a surface that builds its own *Request and routes
// it via Dispatch (bypassing Handle + HeaderParsers) still binds header= params
// from the pre-parser state — matching the /rpc surface and the documented
// invariant. Call it on any sub-request built from an inbound one before
// Dispatch.
func InheritRequestSnapshot(sub, parent *Request) {
	if sub != nil && parent != nil {
		sub.headerSnapshot = parent.headerSnapshot
	}
}

// RecordDispatch emits a DispatchHook event (audit, metrics) for a call a
// surface dispatched OUTSIDE the /rpc HTTP path — an MCP tools/call, say — so
// observability plugins see per-call router/method/status/subject just as they
// do for /rpc, instead of only the opaque outer request. Status, error code, and
// Mode are read from resp; subject from req. No-op when resp is nil.
func (g *Gateway) RecordDispatch(req *Request, router, method, path string, resp *Response, started time.Time) {
	if resp == nil {
		return
	}
	if req != nil {
		// Mark the request so handle skips its generic per-request event — this
		// specific one replaces it, so the call is counted ONCE (parity with a
		// direct /rpc call), not twice (outer /mcp + tool).
		req.recorded = true
	}
	g.recordDispatchEventWithMode(router, method, path, resp.Status, started, subjectOf(req), errCodeFromBody(resp.Body), "", resp.Mode, remoteIPOf(req), requestIDOf(req, resp))
}

// runPluginRoute invokes a plugin route's handler, stamping ModePlugin when the
// handler left Mode unset. Returns nil when the handler DECLINES.
//
// It CONTAINS a panic from the handler — the rpc surface, MCP tools/call, the
// batch/batchstream fan-out, static, explorer, or any custom surface — so a
// single bad request or handler bug can't crash the process. Every HTTP surface
// flows through here (framework endpoints excepted), including the batch/
// batchstream per-entry goroutines, which run OUTSIDE the transport's own
// per-connection recover. The panic is routed through the recovery machinery
// (RecoveryHandler + a logged failure) and converted to a clean 500.
func (g *Gateway) runPluginRoute(r pluginRoute, ctx context.Context, req *Request) (resp *Response) {
	defer func() {
		if rec := recover(); rec != nil {
			failure := HookFailure{
				HookName:   "RouteHandler",
				PluginName: r.owner,
				Err:        fmt.Errorf("panic: %v", rec),
				Panic:      rec,
				Stack:      debug.Stack(),
			}
			resp = g.dispatchRecovery(failure)
			if resp == nil {
				resp = ErrorResponse(rpc.Internal("internal server error"))
			}
			if resp.Mode == "" {
				resp.Mode = ModePlugin
			}
		}
	}()
	resp = r.handler(ctx, req)
	if resp != nil && resp.Mode == "" {
		resp.Mode = ModePlugin
	}
	return resp
}

// errCodeFromBody peeks at a response body looking for {"error":{"code":...}}.
// Cheap parse for the DispatchHook event. Empty string when the body is
// successful or unparseable.
func errCodeFromBody(body []byte) string {
	if len(body) == 0 || body[0] != '{' {
		return ""
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return ""
	}
	return env.Error.Code
}

// codecNameFromContentType maps an inbound Content-Type to a codec registry
// name (HELL-286). Empty Content-Type yields "" — the caller leaves the engine
// default. "application/json" yields "json" EXPLICITLY (not "") so a request
// declaring JSON always resolves to the registered json codec, never to a
// swapped-in SetCodec default — this is what keeps framework sub-dispatches
// (auth verify/check, batch, MCP tools/call), which all send Content-Type:
// application/json, pinned to JSON. Other "application/[x-]<sub>" types yield
// "<sub>" (e.g. application/x-msgpack -> "msgpack"); an unregistered name
// falls back to the default at ResolveCodec time. Parameters (";charset=…")
// are ignored.
func codecNameFromContentType(ct string) string {
	if ct == "" {
		return ""
	}
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	ct = strings.TrimSpace(ct)
	const prefix = "application/"
	if !strings.HasPrefix(ct, prefix) {
		return ""
	}
	// "application/json" -> "json" (an explicit registry name), so JSON is
	// resolved from the codec registry, not the ambient default a consumer may
	// have swapped via SetCodec. Framework sub-dispatches rely on this.
	return strings.TrimPrefix(ct[len(prefix):], "x-")
}

// Dispatch is the mesh fabric. It resolves req's service and routes the call to
// wherever that service lives — the local engine, an in-process peer gateway,
// or a remote pod over HTTP — and returns the response. It is protocol-agnostic:
// ANY surface (the /rpc adapter, MCP tools, batch) builds a Request whose Path
// is /rpc/{service}/{method} and calls Dispatch; the surface never knows or
// cares whether the service is local or across the mesh. That single seam is
// what lets any surface mesh with no surface-specific routing code — a tool
// whose service is federated to another node just resolves remote here.
//
// Dispatch does NOT run the auth/authz middleware: a caller that reaches it
// OUTSIDE the HTTP chain (an MCP tool call, an internal fan-out) owns identity
// (set req.User) and authorization (call Authorize first). The /rpc HTTP path
// still runs the full middleware chain before landing here via the rpc surface.
func (g *Gateway) Dispatch(ctx context.Context, req *Request) *Response {
	router, method, ok := rpc.SplitRPCPath(req.Path)
	if !ok {
		return ErrorResponse(rpc.NotFound("path must be /rpc/{router}/{method}"))
	}
	return g.DispatchResolved(ctx, req, router, method)
}

// DispatchResolved is Dispatch for a caller that has ALREADY parsed req.Path into
// router + method — the rpc surface, for one, parses to apply its POST/reserved
// policy, so it hands the parts straight here instead of re-splitting.
//
// INVARIANT: router and method MUST equal SplitRPCPath(req.Path). Dispatch
// resolves and dispatches by the router/method ARGUMENTS, but dispatchLocal
// stashes req.Path verbatim as ContextKeyPath — pass mismatched values and a
// handler (or an audit/tenant plugin) reading ctx path sees a lie. Callers that
// haven't parsed the path should use Dispatch. See Dispatch for routing.
func (g *Gateway) DispatchResolved(ctx context.Context, req *Request, router, method string) *Response {
	endpoint, ok := g.resolver.Resolve(ctx, router)
	if !ok {
		return ErrorResponse(rpc.NotFound("service %q not registered", router))
	}
	if endpoint.Peer != nil {
		// Nested PEMM: another gateway in the same binary handles this call
		// in-process. Mode label distinguishes peer hops from local engine
		// calls — the peer labels its own response "local", but from THIS
		// gateway's observability perspective the call crossed a peer hop.
		resp := endpoint.Peer(ctx, req)
		if resp == nil {
			return ErrorResponse(rpc.Internal("peer returned nil response"))
		}
		resp.Mode = ModePeer
		return resp
	}
	if endpoint.Local {
		return g.dispatchLocal(ctx, router, method, req)
	}
	return g.dispatchRemote(ctx, endpoint.RemoteAddr, router, method, req)
}

func (g *Gateway) dispatchLocal(ctx context.Context, router, method string, req *Request) *Response {
	rc := rpc.NewContext(ctx)
	// Codec negotiation (HELL-286): map the inbound Content-Type to a
	// registered business codec. An absent Content-Type resolves to the
	// engine default; an explicit "application/json" resolves to the
	// registered json codec (NOT a SetCodec-swapped default), so internal
	// sub-dispatches (auth verify/check, batch, MCP tools/call) — which all
	// send Content-Type: application/json — stay JSON even when a consumer
	// SetCodec'd a non-JSON default. Only negotiate when more than one codec
	// is registered — the common JSON-only deployment skips the Content-Type
	// parse entirely and Dispatch falls back to the default codec.
	var selected rpc.Codec
	if g.engine.Negotiable() {
		if name := codecNameFromContentType(req.Header.Get("Content-Type")); name != "" {
			selected = g.engine.ResolveCodec(name)
			rc.SelectCodec(selected)
		}
	}
	// If the auth middleware resolved Claims, stash them on the context
	// in TWO places: rc.User as the canonical "who is the caller" value
	// (so rpc.UserFromContext works), and rc.State["sov.claims"] as the
	// full structured Claims (so handlers can read Role/Scopes via
	// gateway.ClaimsFromContext).
	if claims, ok := req.User.(*Claims); ok && claims != nil {
		rc.User = claims.Subject
		rc.Set(ContextKeyClaims, claims)
	} else {
		rc.User = req.User
	}
	rc.Set(ContextKeyRemoteIP, req.RemoteIP)
	rc.Set(ContextKeyPath, req.Path)
	// Expose the pre-parser header snapshot to the engine's header= param
	// binding (rpc.CtxHeaderGetter), so a bound param matches what the authz
	// gate saw. Gated so the common all-body deployment pays no alloc; falls
	// back to live req.Header for any dispatch that didn't pass through handle
	// (e.g. an internal sub-dispatch).
	if g.engine.NeedsHeaderGetter() {
		src := req.headerSnapshot
		if src == nil {
			src = req.Header
		}
		rc.Set(rpc.CtxHeaderGetter, rpc.HeaderGetter(src.Get))
	}
	// Stash the inbound Authorization header so handlers can forward it
	// on cross-service calls (e.g. mesh-mode FeedRouter calling back
	// through the central gateway). The gateway has already validated
	// the bearer; this is pass-through, not re-verification.
	if auth := req.Header.Get("Authorization"); auth != "" {
		rc.Set(ContextKeyAuthorization, auth)
	}
	// Plugin hook: ContextContributors stash per-request metadata on
	// rc so in-process handlers see the same values plugins added to
	// outbound HTTP headers (request-id, trace-id, tenant). Symmetric
	// to HeaderInjector for the local path.
	g.callContextContributors(rc, req)
	status, body := g.engine.Dispatch(rc, router, method, req.Body)
	resp := &Response{Status: status, Body: body, Mode: ModeLocal}
	// Reflect the negotiated codec on the response so the caller decodes the
	// body with the same codec it used (HELL-286). Skip json — the adapter
	// already defaults application/json — and skip when the requested codec
	// fell back to the default, so Content-Type never lies about the body.
	if selected != nil && selected.Name() != "json" {
		resp.Header = Header{"Content-Type": "application/x-" + selected.Name()}
	}
	return resp
}

// CodecFor returns the business codec negotiated for req by its Content-Type,
// or the JSON default when none/unknown. Exposed (HELL-286) so a codec-aware
// plugin can encode per-entry values with the same codec the caller used,
// instead of hardcoding JSON.
func (g *Gateway) CodecFor(req *Request) rpc.Codec {
	return g.engine.ResolveCodec(codecNameFromContentType(req.Header.Get("Content-Type")))
}

func (g *Gateway) dispatchRemote(ctx context.Context, base, router, method string, req *Request) *Response {
	url := strings.TrimRight(base, "/") + req.Path
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(req.Body))
	if err != nil {
		return ErrorResponse(rpc.Internal("proxy build request: %v", err))
	}
	// Forward inbound headers (already X-Sov-* stripped by Server).
	for k, v := range req.Header {
		hreq.Header.Set(k, v)
	}
	// Inject verified claim headers — the downstream service trusts these
	// because (a) the network is internal and (b) optionally HMAC-sealed.
	g.injectClaimHeaders(hreq, req)
	if req.RemoteIP != "" {
		hreq.Header.Set("X-Forwarded-For", req.RemoteIP)
	}
	// Plugin hook: HeaderInjectors fire on every outbound proxy hop.
	// X-Sov-Upstream is no longer framework-stamped; the Advertise
	// plugin owns that header now.
	g.callHeaderInjectors(ctx, req, hreq)
	stampDeadline(ctx, hreq)

	// Circuit breaker: after repeated transport failures to this upstream,
	// fail fast instead of making every call wait out the full proxy timeout.
	if !g.breakers.allow(base) {
		return ErrorResponse(&rpc.Error{
			Status: http.StatusServiceUnavailable, Code: "UPSTREAM_CIRCUIT_OPEN",
			Message: fmt.Sprintf("upstream %s circuit open (failing fast after repeated errors)", base),
		})
	}
	resp, err := g.proxy.Do(hreq)
	if err != nil {
		g.breakers.record(base, false)
		return ErrorResponse(&rpc.Error{
			Status: http.StatusBadGateway, Code: "UPSTREAM_UNAVAILABLE",
			Message: fmt.Sprintf("proxy %s/%s: %v", router, method, err),
		})
	}
	// A 5xx from the upstream is an unhealthy signal too (panicking handler,
	// dependency down, overload) — count it as a breaker failure so an
	// up-but-broken pod trips the circuit, not just an unreachable one. 4xx is
	// the caller's fault, not the upstream's health, so it counts as success.
	g.breakers.record(base, resp.StatusCode < 500)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	hdr := Header{}
	for k, v := range resp.Header {
		hdr[k] = strings.Join(v, ",")
	}
	mode := ModeRemote
	if resp.Header.Get(IntrospectVisitedHeader) != "" {
		mode = ModeFederated
	}
	return &Response{Status: resp.StatusCode, Header: hdr, Body: body, Mode: mode}
}

// BuildProxyRequest constructs an outbound HTTP request to addr+path
// pre-populated with the parent request's forwarded headers, the
// injected X-Sov-* claim bundle, the forwarded-for IP, and every
// registered HeaderInjector. Plugin authors (batch, custom proxy)
// use this so their outbound calls participate in the same
// header-injection chain as the framework's own dispatchRemote.
func (g *Gateway) BuildProxyRequest(ctx context.Context, method, addr, path string, body []byte, parent *Request) (*http.Request, error) {
	url := strings.TrimRight(addr, "/") + path
	hreq, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if parent != nil {
		for k, v := range parent.Header {
			hreq.Header.Set(k, v)
		}
		g.injectClaimHeaders(hreq, parent)
		if parent.RemoteIP != "" {
			hreq.Header.Set("X-Forwarded-For", parent.RemoteIP)
		}
		g.callHeaderInjectors(ctx, parent, hreq)
		stampDeadline(ctx, hreq)
	}
	if g.advertiseURL != "" {
		hreq.Header.Set("X-Sov-Upstream", g.advertiseURL)
	}
	hreq.Header.Set("Content-Type", "application/json")
	return hreq, nil
}

// routerWireName returns the wire-side name of a router pointer by
// stripping the "Router" suffix from the underlying type name.
func routerWireName(router any) string {
	t := reflect.TypeOf(router)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	name := t.Name()
	const suffix = "Router"
	if strings.HasSuffix(name, suffix) {
		return strings.TrimSuffix(name, suffix)
	}
	return name
}

// ErrorResponse builds a *Response from an *rpc.Error: the error's HTTP
// status plus its JSON-marshaled envelope body. Plugins returning an
// error from a route handler should use this so the wire shape matches
// the framework's own error responses.
func ErrorResponse(e *rpc.Error) *Response {
	return &Response{Status: e.Status, Body: rpc.MarshalError(e)}
}
