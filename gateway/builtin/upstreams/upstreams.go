// Package upstreams limits which upstream gateway URLs a pod trusts
// X-Sov-* claim headers from. When TrustUpstreamClaims is enabled AND
// this plugin is registered, the trust guard strips inbound X-Sov-*
// from any caller whose X-Sov-Upstream header is not on the list —
// the request proceeds as anonymous, not 401.
//
// Plugin owns the allowlist + the decision via UpstreamTrustPolicy.
// Framework holds no upstream-allowlist state.
//
//	gw.Use(upstreams.New("http://prime:8080", "http://edge:8080"))
package upstreams

import (
	"github.com/Toyz/sov/gateway"
)

// advertiseHeader is the canonical name the upstream gateway stamps
// (via sov.WithAdvertiseURL). Kept here so upstreams stays the only
// consumer of the constant after the standalone advertise plugin was
// folded into the gateway core.
const advertiseHeader = "X-Sov-Upstream"

// Config configures upstreams. Allowed is the list of upstream
// gateway URLs whose X-Sov-* claim bundles will be trusted. URLs
// are normalized at construction; invalid URLs panic.
type Config struct {
	Allowed []string
}

// Plugin is the trust-guard-allowlist owner returned by New.
type Plugin struct{ allow map[string]struct{} }

// Compile-time proof of the hooks this plugin binds — a signature
// drift here is a build error, not a silent non-binding at runtime.
var (
	_ gateway.Plugin              = (*Plugin)(nil)
	_ gateway.PluginDoc           = (*Plugin)(nil)
	_ gateway.UpstreamTrustPolicy = (*Plugin)(nil)
)

// New returns an upstream-allowlist plugin from cfg.
func New(cfg Config) *Plugin {
	m := make(map[string]struct{}, len(cfg.Allowed))
	for _, raw := range cfg.Allowed {
		canon, err := gateway.NormalizeUpstreamURL(raw)
		if err != nil {
			panic("upstreams.New: " + err.Error())
		}
		m[canon] = struct{}{}
	}
	return &Plugin{allow: m}
}

// PluginName surfaces in /rpc/_introspect.plugins[].
func (p *Plugin) PluginName() string { return "upstream-gateways" }

// Doc surfaces a one-line description in /rpc/_introspect + the explorer.
func (p *Plugin) Doc() string {
	return "Trusts inbound X-Sov-* claim headers only from an allowlist of upstream gateway URLs."
}

// TrustUpstream returns true iff the inbound X-Sov-Upstream header
// matches one of the allowlisted URLs (after normalization). Empty
// allowlist returns false — register the plugin only when you intend
// to enforce. Missing header returns false.
func (p *Plugin) TrustUpstream(headers map[string][]string) bool {
	if len(p.allow) == 0 {
		return false
	}
	vals := headers[advertiseHeader]
	if len(vals) == 0 {
		return false
	}
	canon, err := gateway.NormalizeUpstreamURL(vals[0])
	if err != nil {
		return false
	}
	_, ok := p.allow[canon]
	return ok
}
