package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/Toyz/sov/rpc"
)

// handle is the single RequestHandler the Server invokes per request.
// Order of operations:
//  1. Framework endpoints (/rpc/_health, /rpc/_introspect, /rpc/_batch,
//     /rpc/_register) — dispatched against gateway-owned handlers.
//  2. Path validation — must be /rpc/{router}/{method}, POST only.
//  3. Reject service-level _X (refused at gateway by design).
//  4. Resolve the router and dispatch local or remote.
func (g *Gateway) handle(ctx context.Context, req *Request) *Response {
	// Plugin hook: HeaderParser runs on every inbound request before
	// any routing decision. A parser may short-circuit by returning an
	// error; the typical use is stashing values onto req.Header or
	// req.User without erroring.
	started := time.Now()
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
	router, method, _ := rpc.SplitRPCPath(req.Path)
	subject := ""
	if s, ok := req.User.(string); ok {
		subject = s
	} else if c, ok := req.User.(*Claims); ok && c != nil {
		subject = c.Subject
	}
	g.recordDispatchEventWithMode(router, method, req.Path, resp.Status, started, subject, errCodeFromBody(resp.Body), "", resp.Mode)
	return resp
}

func (g *Gateway) handleInner(ctx context.Context, req *Request) *Response {
	if resp := g.frameworkEndpoint(ctx, req); resp != nil {
		if resp.Mode == "" {
			resp.Mode = ModeFramework
		}
		return resp
	}
	// Plugin-owned routes via RouteHandler. Registered after framework
	// endpoints so a plugin cannot shadow /rpc/_health, _introspect,
	// _batch. A handler that returns nil DECLINES the request — routing
	// falls through to business dispatch. This lets a broad catch-all
	// mount (e.g. a static SPA plugin at "/") coexist with business RPC
	// routes by declining the paths it doesn't own (e.g. "/rpc/...").
	if route, ok := g.matchPluginRoute(req.Path); ok {
		if resp := route.handler(ctx, req); resp != nil {
			if resp.Mode == "" {
				resp.Mode = ModePlugin
			}
			return resp
		}
	}
	return g.routeBusiness(ctx, req)
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

// routeBusiness handles non-framework /rpc/{router}/{method} dispatch.
// Exported-ish (lowercase but used from framework.go) so _batch can fan
// out through the same path.
func (g *Gateway) routeBusiness(ctx context.Context, req *Request) *Response {
	if req.Method != "" && req.Method != http.MethodPost {
		return ErrorResponse(&rpc.Error{Status: 405, Code: "BAD_REQUEST", Message: "method not allowed"})
	}
	router, method, ok := rpc.SplitRPCPath(req.Path)
	if !ok {
		return ErrorResponse(rpc.NotFound("path must be /rpc/{router}/{method}"))
	}
	if strings.HasPrefix(router, "_") {
		return ErrorResponse(rpc.NotFound("router %q reserved", router))
	}
	if strings.HasPrefix(method, "_") {
		return ErrorResponse(rpc.NotFound("method %q is internal-network only", method))
	}

	endpoint, ok := g.resolver.Resolve(ctx, router)
	if !ok {
		return ErrorResponse(rpc.NotFound("service %q not registered", router))
	}
	if endpoint.Peer != nil {
		// Nested PEMM: another gateway in the same binary handles
		// this call in-process. Mode label distinguishes peer hops
		// from local engine calls.
		resp := endpoint.Peer(ctx, req)
		if resp == nil {
			return ErrorResponse(rpc.Internal("peer returned nil response"))
		}
		// Always overwrite Mode — the peer's own dispatch labels its
		// own response (typically "local"), but from THIS gateway's
		// observability perspective the call crossed a peer hop. The
		// peer gateway's audit (if installed) still saw it as local.
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
	return &Response{Status: status, Body: body, Mode: ModeLocal}
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

	resp, err := g.proxy.Do(hreq)
	if err != nil {
		return ErrorResponse(&rpc.Error{
			Status: http.StatusBadGateway, Code: "UPSTREAM_UNAVAILABLE",
			Message: fmt.Sprintf("proxy %s/%s: %v", router, method, err),
		})
	}
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
