package rpc

import "time"

// Claims is the verified caller identity the AuthService returns. Shape
// is fixed by the framework so every downstream service knows what to
// expect — both on the wire (X-Sov-* headers, decomposed by the
// gateway) and in-process (ctx.Claims()).
//
// Claims is identity + delegation only. Authorization state (role
// memberships, RBAC matrix) is queried by the AuthzService at decision
// time, not stamped here at issue time. Stuffing roles into Claims
// turns ambient identity into ambient policy and makes hot-reload of
// RBAC impossible — keep them separate.
type Claims struct {
	Subject   string         `json:"sub"`              // opaque caller id
	Issuer    string         `json:"iss,omitempty"`    // which AuthService minted this
	Scopes    []string       `json:"scopes,omitempty"` // OAuth scope semantics — what powers the TOKEN grants
	ExpiresAt time.Time      `json:"exp"`              // cache TTL upper bound
	Extra     map[string]any `json:"extra,omitempty"`  // escape hatch (tenant_id, device_id, ...)
}

// ContextKeyClaims is the key under which the gateway stashes *Claims
// on rpc.Context.State during local dispatch. Handlers reach in via
// (*Context).Claims() — never type-assert the value directly.
const ContextKeyClaims = "sov.claims"

// Claims returns the gateway-stamped *Claims from the request context,
// or nil if the caller is anonymous (no bearer, or the AuthService
// returned nothing). Typed accessor — handlers don't import the gateway
// package just to read identity.
func (c *Context) Claims() *Claims {
	if c == nil {
		return nil
	}
	if v := c.Get(ContextKeyClaims); v != nil {
		if cl, ok := v.(*Claims); ok {
			return cl
		}
	}
	return nil
}

// RequireSubject returns the subject id stashed on ctx.User by the
// gateway, or 401 UNAUTHORIZED if the request was anonymous. The
// canonical one-liner gate at the top of any handler that needs an
// authenticated caller:
//
//	uid, err := rpc.RequireSubject(ctx)
//	if err != nil { return nil, err }
//
// Identity-required policy lives next to the method that needs it,
// not centralized in main(). The AuthzService (when bound on the
// gateway) is the other layer: it sees every request including
// anonymous ones and can return {Authenticate: true} to surface 401
// before dispatch.
func RequireSubject(c *Context) (string, error) {
	if c == nil || c.User == nil {
		return "", Unauthorized("authentication required")
	}
	s, ok := c.User.(string)
	if !ok || s == "" {
		return "", Unauthorized("authentication required")
	}
	return s, nil
}
