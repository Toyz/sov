package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Toyz/sov/rpc"
)

// Claims is the verified caller identity. Aliased to rpc.Claims so
// handlers can read identity via ctx.Claims() without importing the
// gateway package. The wire shape (X-Sov-* headers, seal) is owned by
// the gateway; the Go type lives in rpc/.
type Claims = rpc.Claims

// ContextKeyClaims is re-exported from rpc so existing gateway callers
// keep working. Prefer ctx.Claims() in handler code.
const ContextKeyClaims = rpc.ContextKeyClaims

// ContextKeyAuthorization is where dispatchLocal stashes the verbatim
// inbound Authorization header. Cross-service callers (e.g. a mesh
// pod's HTTP-backed Client) forward this back to the gateway for the
// downstream call so the impersonation chain is intact.
const ContextKeyAuthorization = "sov.authorization"

// ContextKeyRemoteIP and ContextKeyPath are where dispatchLocal stashes
// the caller IP and the inbound request path for handlers that need them
// via ctx.Get(...). Constants so callers don't repeat the literals.
const (
	ContextKeyRemoteIP = "http.remoteIP"
	ContextKeyPath     = "gateway.path"
)

// ClaimsFromContext returns the gateway-stamped Claims, or nil. Prefer
// ctx.Claims() in handlers; this helper exists for callers that already
// have a *rpc.Context and don't want the method syntax.
func ClaimsFromContext(ctx *rpc.Context) *Claims { return ctx.Claims() }

// AuthorizationFromContext returns the verbatim inbound bearer header
// the gateway stashed during dispatch. Cross-service callers forward
// this on subsequent RPCs so identity propagates through call chains.
// Typed accessor — consumers never reach for ctx.Get(ContextKeyAuthorization)
// directly.
func AuthorizationFromContext(ctx *rpc.Context) string {
	if v := ctx.Get(ContextKeyAuthorization); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// AuthService is the contract gw.RegisterAuth requires. The wire name
// is derived from the implementing struct's type name (strip "Router"
// suffix); the method is always "verify". Verify resolves a bearer
// token to identity-only Claims — never to a profile row.
type AuthService interface {
	Verify(ctx *rpc.Context, p *VerifyParams) (*Claims, error)
}

// AuthzService is the contract gw.RegisterAuthz requires. The method
// is always "check"; the wire name comes from the struct type.
//
// The gateway calls Check on EVERY request when an AuthzService is
// bound — including anonymous requests (claims == nil). This makes the
// authz service the single source of truth for "this method requires
// auth" vs "this method requires admin scope" vs "this method is
// public". Returning {Allow:false, Authenticate:true} tells the gateway
// to surface 401 not 403, so the same primitive expresses both
// authentication-required and authorization-denied without a second
// hop.
type AuthzService interface {
	Check(ctx *rpc.Context, p *CheckParams) (*AuthzDecision, error)
}

// VerifyParams is the request payload the gateway sends to
// AuthService.Verify.
type VerifyParams struct {
	Token string `json:"token"`
}

// CheckParams is the request payload the gateway sends to
// AuthzService.Check. Claims is nil for anonymous requests; the authz
// service is expected to handle that case (typically by returning
// {Allow:false, Authenticate:true} for non-public methods).
type CheckParams struct {
	Claims  *Claims `json:"claims,omitempty"`
	Service string  `json:"service"`
	Method  string  `json:"method"`
}

// AuthBinding records which registered service is the auth verifier. A
// gateway has at most one.
type AuthBinding struct {
	Service string
	Method  string
}

// AuthzBinding records the policy-as-service binding, if any. The
// gateway calls Service/Method with the resolved Claims (or nil for
// anonymous) plus the downstream {service, method} the caller wants to
// invoke; the response is `{allow, authenticate, reason}`.
type AuthzBinding struct {
	Service string
	Method  string
}

// AuthzDecision is the shape the registered authz service must return.
//
//   - Allow=true              → request proceeds.
//   - Allow=false             → request denied. Gateway returns 403 FORBIDDEN
//     with Reason as the message — unless
//     Authenticate=true.
//   - Allow=false, Authenticate=true
//     → gateway returns 401 UNAUTHORIZED with
//     Reason as the message. Use this when the
//     method requires a logged-in caller and
//     none is present (anonymous claims).
type AuthzDecision struct {
	Allow        bool   `json:"allow"`
	Reason       string `json:"reason,omitempty"`
	Authenticate bool   `json:"authenticate,omitempty"`
}

// ClaimsCache caches verified Claims keyed by the raw bearer token so the
// gateway skips AuthService.verify on repeat requests. The default impl is
// in-memory (per-replica). Implement this and pass WithClaimsCache to back
// it with Redis/memcached so a fleet of gateway replicas shares verified
// results instead of each re-verifying.
//
// SECURITY: the token is a secret. Never log it. In a shared/remote store,
// hash the key (e.g. SHA-256) rather than storing raw tokens, and set the
// entry TTL from claims.ExpiresAt so a cached identity can't outlive the
// token. The gateway independently re-checks ExpiresAt after Get, so a
// stale entry is never honored even if an impl forgets to expire it.
type ClaimsCache interface {
	// Get returns cached Claims for token, or ok=false on miss.
	Get(token string) (claims *Claims, ok bool)
	// Put stores Claims for token. Honor claims.ExpiresAt for eviction.
	Put(token string, claims *Claims)
}

// memClaimsCache is the default in-memory ClaimsCache. Token is used only
// as a map key, never logged.
type memClaimsCache struct {
	mu      sync.Mutex
	entries map[string]*Claims
}

func newMemClaimsCache() *memClaimsCache { return &memClaimsCache{entries: map[string]*Claims{}} }

func (c *memClaimsCache) Get(token string) (*Claims, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cl, ok := c.entries[token]
	if !ok {
		return nil, false
	}
	if !cl.ExpiresAt.IsZero() && time.Now().UTC().After(cl.ExpiresAt) {
		delete(c.entries, token)
		return nil, false
	}
	return cl, true
}

func (c *memClaimsCache) Put(token string, cl *Claims) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[token] = cl
}

// verifyToken calls the configured AuthService.{verify} via the
// gateway's own dispatch path so it transparently works for both
// in-process and remote auth services. Returns Claims on success.
func (g *Gateway) verifyToken(ctx context.Context, token string) (*Claims, error) {
	if g.authBinding == nil {
		return nil, rpc.Internal("auth binding not configured")
	}
	if cl, ok := g.authCache.Get(token); ok {
		// Re-check expiry here so a sloppy custom cache can't hand back a
		// claim that outlived its token.
		if cl.ExpiresAt.IsZero() || time.Now().UTC().Before(cl.ExpiresAt) {
			return cl, nil
		}
	}
	body, _ := json.Marshal(rpc.Request{
		Args: mustJSON(VerifyParams{Token: token}),
	})
	sub := &Request{
		Method: http.MethodPost,
		Path:   "/rpc/" + g.authBinding.Service + "/" + g.authBinding.Method,
		Header: Header{}, // never forward inbound headers for the verify call
		Body:   body,
	}
	resp := g.routeBusiness(ctx, sub)
	if resp.Status >= 400 {
		return nil, claimsErrorFromBody(resp)
	}
	var env struct {
		Data Claims `json:"data"`
	}
	if err := json.Unmarshal(resp.Body, &env); err != nil {
		return nil, rpc.Internal("decode claims: %v", err)
	}
	if env.Data.Subject == "" {
		return nil, rpc.Unauthorized("auth service returned no subject")
	}
	g.authCache.Put(token, &env.Data)
	return &env.Data, nil
}

// checkAuthz calls the configured AuthzService.{check}. Returns nil if
// allowed, an Error otherwise. Called on every request (including
// anonymous ones) when an authz binding is configured.
func (g *Gateway) checkAuthz(ctx context.Context, claims *Claims, service, method string) error {
	if g.authzBinding == nil {
		return nil
	}
	body, _ := json.Marshal(rpc.Request{
		Args: mustJSON(CheckParams{Claims: claims, Service: service, Method: method}),
	})
	sub := &Request{
		Method: http.MethodPost,
		Path:   "/rpc/" + g.authzBinding.Service + "/" + g.authzBinding.Method,
		Header: Header{},
		Body:   body,
	}
	resp := g.routeBusiness(ctx, sub)
	if resp.Status >= 400 {
		return claimsErrorFromBody(resp)
	}
	var env struct {
		Data AuthzDecision `json:"data"`
	}
	if err := json.Unmarshal(resp.Body, &env); err != nil {
		return rpc.Internal("decode authz decision: %v", err)
	}
	if env.Data.Allow {
		return nil
	}
	reason := env.Data.Reason
	if reason == "" {
		if env.Data.Authenticate {
			reason = "authentication required"
		} else {
			reason = "denied by policy"
		}
	}
	if env.Data.Authenticate {
		return rpc.Unauthorized("%s", reason)
	}
	return rpc.Forbidden("%s", reason)
}

// authMiddleware runs first in the middleware chain. It strips the
// inbound bearer, looks up the cached Claims, calls AuthService.verify
// on miss, and stamps the Claims on req.User so downstream dispatch
// and proxy can use them.
//
// Calls that should bypass auth entirely:
//   - The auth service itself (otherwise infinite recursion).
//   - Framework endpoints OTHER than /rpc/_batch — the gateway owns
//     those (health, register, introspect, explorer) and decides
//     internally whether to enforce. /rpc/_batch must resolve the
//     bearer so per-entry sub-requests inherit the Claims; the batch
//     handler then re-applies authz per entry inside the fan-out.
//
// If no Authorization header is present, the call proceeds with
// req.User = nil. Authz middleware (if configured) handles
// "anonymous-needs-401" via {Authenticate:true} decisions; handlers
// without an authz service in front of them use rpc.RequireSubject(ctx)
// to gate themselves.
// pathBatch is the one framework endpoint the auth middleware does NOT
// bypass — batch entries dispatch through the full chain, so the batch
// request itself must carry verified claims.
const pathBatch = "/rpc/_batch"

// isFrameworkPath reports whether p is a reserved /rpc/_* endpoint.
func isFrameworkPath(p string) bool { return strings.HasPrefix(p, "/rpc/_") }

// rpcPath builds the canonical /rpc/{service}/{method} path used for the
// auth/authz recursion-guard comparisons.
func rpcPath(service, method string) string { return "/rpc/" + service + "/" + method }

func (g *Gateway) authMiddleware() Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, req *Request) *Response {
			if isFrameworkPath(req.Path) && req.Path != pathBatch {
				return next(ctx, req)
			}

			// 1. Upstream-injected claims: if the Server let X-Sov-*
			//    headers through (TrustUpstreamClaims=true) AND the
			//    upstream gateway already injected them, lift them onto
			//    req.User. This is how pod handlers see ctx.Claims()
			//    without each pod wiring its own header-reading middleware.
			if req.User == nil {
				if c := ClaimsFromHeaders(headerToHTTPHeader(req.Header)); c != nil {
					req.User = c
				}
			}

			// 2. Bearer verification: if a verifier is bound AND the
			//    caller sent a bearer, resolve Claims via the auth
			//    service. Never recurse — calls to the auth service
			//    itself bypass this step.
			if g.authBinding != nil && req.Path != rpcPath(g.authBinding.Service, g.authBinding.Method) {
				if tok := bearerToken(req.Header); tok != "" {
					claims, err := g.verifyToken(ctx, tok)
					if err != nil {
						return ErrorResponseFromAny(err)
					}
					req.User = claims
				}
			}

			// 3. Plugin hook: AuthTranslator runs once after Claims are
			//    resolved (or stays nil for anonymous). Translators
			//    typically copy claim values to legacy headers so a
			//    brownfield downstream service that expects
			//    X-Forwarded-User / X-Remote-Email gets fed.
			claims, _ := req.User.(*Claims)
			g.callAuthTranslators(req, claims)

			return next(ctx, req)
		}
	}
}

// headerToHTTPHeader adapts gateway.Header → http.Header so the typed
// accessors (ClaimsFromHeaders, VerifySeal) work uniformly.
func headerToHTTPHeader(h Header) http.Header {
	out := http.Header{}
	for k, v := range h {
		out.Set(k, v)
	}
	return out
}

// authzMiddleware runs AFTER authMiddleware. It calls the registered
// authz service (if any) on EVERY request — including anonymous ones,
// so the authz service is the single source of truth for "this method
// requires auth" vs "this method requires admin scope" vs "this method
// is public". The decision shape's Authenticate flag drives 401 vs 403.
func (g *Gateway) authzMiddleware() Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, req *Request) *Response {
			if g.authzBinding == nil {
				return next(ctx, req)
			}
			if isFrameworkPath(req.Path) {
				return next(ctx, req)
			}
			if req.Path == rpcPath(g.authzBinding.Service, g.authzBinding.Method) {
				return next(ctx, req)
			}
			// Bypass the auth service's own endpoints — the gateway
			// invokes them as part of verify and they must never gate
			// themselves through authz.
			if g.authBinding != nil && req.Path == rpcPath(g.authBinding.Service, g.authBinding.Method) {
				return next(ctx, req)
			}
			claims, _ := req.User.(*Claims)
			router, method, ok := rpc.SplitRPCPath(req.Path)
			if !ok {
				return next(ctx, req)
			}
			if err := g.checkAuthz(ctx, claims, router, method); err != nil {
				return ErrorResponseFromAny(err)
			}
			return next(ctx, req)
		}
	}
}

// bearerToken extracts the token from `Authorization: Bearer <token>`,
// returning "" if absent or malformed.
func bearerToken(h Header) string {
	v := h.Get("Authorization") // Header.Get is case-insensitive
	if !strings.HasPrefix(v, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(v, "Bearer ")
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func ErrorResponseFromAny(err error) *Response {
	if rerr, ok := err.(*rpc.Error); ok {
		return ErrorResponse(rerr)
	}
	return ErrorResponse(rpc.Internal("%v", err))
}

func claimsErrorFromBody(resp *Response) error {
	if e, ok := rpc.DecodeErrorBody(resp.Body, resp.Status); ok {
		return e
	}
	return &rpc.Error{Status: resp.Status, Code: "INTERNAL", Message: "auth verify failed"}
}
