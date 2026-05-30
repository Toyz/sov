package gateway

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Header name constants for the injected claim bundle. Identity-only —
// no role/permission header. Role lookup happens at the AuthzService at
// decision time; per-request claim bundles never carry authorization
// state. These four are framework-owned because every sov gateway
// speaks them on the wire; the seal (X-Sov-Seal) is the hmacseal
// plugin's contract — see gateway/builtin/hmacseal/proto.
const (
	HeaderSubject = "X-Sov-Subject"
	HeaderIssuer  = "X-Sov-Issuer"
	HeaderScopes  = "X-Sov-Scopes"  // comma-joined OAuth scopes
	HeaderExpires = "X-Sov-Expires" // unix seconds
)

// injectClaimHeaders writes verified Claims into the outbound HTTP
// request as X-Sov-* headers. Called only by remote-proxy dispatch.
// The seal (X-Sov-Seal) is NOT written here — the hmacseal plugin
// owns that, fired as a HeaderInjector after this and after any other
// plugin's header writes so the seal covers everything.
func (g *Gateway) injectClaimHeaders(hreq *http.Request, req *Request) {
	claims, ok := req.User.(*Claims)
	if !ok || claims == nil {
		return
	}
	if claims.Subject != "" {
		hreq.Header.Set(HeaderSubject, claims.Subject)
	}
	if claims.Issuer != "" {
		hreq.Header.Set(HeaderIssuer, claims.Issuer)
	}
	if len(claims.Scopes) > 0 {
		hreq.Header.Set(HeaderScopes, strings.Join(claims.Scopes, ","))
	}
	if !claims.ExpiresAt.IsZero() {
		hreq.Header.Set(HeaderExpires, strconv.FormatInt(claims.ExpiresAt.UTC().Unix(), 10))
	}
}

// ClaimsFromHeaders parses X-Sov-* headers into a *Claims. Returns nil
// if no X-Sov-Subject is present. Typed accessor — downstream services
// reach for this instead of `h.Get(gateway.HeaderSubject)` + string
// gymnastics. Pair with the hmacseal/proto.Verify call when an HMAC
// secret is configured so forged claim headers can be rejected.
func ClaimsFromHeaders(h http.Header) *Claims {
	sub := h.Get(HeaderSubject)
	if sub == "" {
		return nil
	}
	c := &Claims{Subject: sub, Issuer: h.Get(HeaderIssuer)}
	if s := h.Get(HeaderScopes); s != "" {
		for _, scope := range strings.Split(s, ",") {
			scope = strings.TrimSpace(scope)
			if scope != "" {
				c.Scopes = append(c.Scopes, scope)
			}
		}
	}
	if exp := h.Get(HeaderExpires); exp != "" {
		if ts, err := strconv.ParseInt(exp, 10, 64); err == nil {
			c.ExpiresAt = time.Unix(ts, 0).UTC()
		}
	}
	return c
}
