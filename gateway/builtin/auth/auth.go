// Package auth ships a first-party sov plugin that translates
// verified Claims into legacy-shape headers
// ("X-Forwarded-User" / "X-Forwarded-Scopes" / "X-Tenant-Id") on
// every outbound proxy hop. Use when downstream services in your
// stack expect those header names instead of (or in addition to)
// the X-Sov-* bundle the gateway already injects.
//
//	gw.Use(auth.New())
//
// AuthTranslator fires ONCE per inbound request after authMiddleware
// resolves Claims. The gateway then forwards req.Header on every
// dispatchRemote / dispatchRemoteBatch hop, so the translated
// headers reach every downstream pod without per-handler code.
package auth

import (
	"net/http"
	"strings"

	"github.com/Toyz/sov/gateway"
)

// Config configures the auth header-translation plugin. Zero values
// use the standard X-Forwarded-* defaults. Set a field to "-" to
// disable that specific stamp.
type Config struct {
	SubjectHeader string // default "X-Forwarded-User"; "-" to disable
	TenantHeader  string // default "X-Tenant-Id"; "-" to disable
	ScopesHeader  string // default "X-Forwarded-Scopes"; "-" to disable
}

// Plugin is the auth header-translation plugin.
type Plugin struct {
	subjectHeader string
	tenantHeader  string
	scopesHeader  string
}

// Compile-time proof of the hooks this plugin binds — a signature
// drift here is a build error, not a silent non-binding at runtime.
var (
	_ gateway.Plugin         = (*Plugin)(nil)
	_ gateway.PluginDoc      = (*Plugin)(nil)
	_ gateway.AuthTranslator = (*Plugin)(nil)
)

// New returns the plugin from cfg.
func New(cfg Config) *Plugin {
	pick := func(v, def string) string {
		switch v {
		case "":
			return def
		case "-":
			return ""
		default:
			return v
		}
	}
	return &Plugin{
		subjectHeader: pick(cfg.SubjectHeader, "X-Forwarded-User"),
		tenantHeader:  pick(cfg.TenantHeader, "X-Tenant-Id"),
		scopesHeader:  pick(cfg.ScopesHeader, "X-Forwarded-Scopes"),
	}
}

// PluginName satisfies gateway.Plugin.
func (p *Plugin) PluginName() string { return "auth" }

// Doc surfaces a one-line description in /rpc/_introspect + the explorer.
func (p *Plugin) Doc() string {
	return "Example bearer-auth verifier (in-memory credentials + sessions); issuer-agnostic."
}

// TranslateAuth satisfies gateway.AuthTranslator. Anonymous requests
// (claims == nil) early-return — the legacy headers express identity,
// not presence.
func (p *Plugin) TranslateAuth(req *gateway.Request, claims *gateway.Claims) error {
	if claims == nil {
		return nil
	}
	if req.Header == nil {
		req.Header = gateway.Header{}
	}
	if p.subjectHeader != "" && claims.Subject != "" {
		req.Header[http.CanonicalHeaderKey(p.subjectHeader)] = claims.Subject
	}
	if p.tenantHeader != "" {
		// Claims.Tenant lands in wave 9; guard via Extra for now.
		if t, ok := claims.Extra["tenant"].(string); ok && t != "" {
			req.Header[http.CanonicalHeaderKey(p.tenantHeader)] = t
		}
	}
	if p.scopesHeader != "" && len(claims.Scopes) > 0 {
		req.Header[http.CanonicalHeaderKey(p.scopesHeader)] = strings.Join(claims.Scopes, ",")
	}
	return nil
}
