package gateway

import (
	"context"
	"encoding/json"

	"github.com/Toyz/sov/rpc"
)

// ConfigReport is the sanitized /rpc/_config body: the gateway's effective
// operational knobs plus the set of wired plugins and locally-served routers.
//
// SANITIZED BY CONSTRUCTION. It names only scalar tuning knobs and plugin/
// router NAMES — never a plugin's Config. Mesh secrets, register tokens, TLS
// keys and bearer material live inside individual plugins and are never read
// here, so no secret, key, or token can appear in the report. Adding a field
// that reaches into a plugin's config would break that invariant.
type ConfigReport struct {
	MaxInFlight       int64         `json:"max_in_flight"` // load-shed cap; 0 = unlimited
	InFlight          int64         `json:"in_flight"`     // requests currently in the dispatch chain
	AdvertiseURL      string        `json:"advertise_url,omitempty"`
	TrustUpstream     bool          `json:"trust_upstream_claims"`
	IntrospectExposed bool          `json:"introspect_exposed"`
	AuthConfigured    bool          `json:"auth_configured"`
	AuthzConfigured   bool          `json:"authz_configured"`
	CustomClaimsCache bool          `json:"custom_claims_cache"` // a non-default ClaimsCache is installed
	RemoteBreaker     BreakerReport `json:"remote_breaker"`
	Plugins           []string      `json:"plugins"`
	Routers           []string      `json:"routers"`
}

// BreakerReport is the effective per-upstream circuit-breaker config.
type BreakerReport struct {
	Disabled         bool   `json:"disabled"`
	FailureThreshold int    `json:"failure_threshold"`
	Cooldown         string `json:"cooldown"` // human-readable duration
}

// ExposeConfig opens the opt-in /rpc/_config endpoint. Called by the builtin
// configdump plugin's Apply; off by default because the report, while
// sanitized, still discloses operational topology. Idempotent.
func (g *Gateway) ExposeConfig() { g.configExposed = true }

// ConfigExposed reports whether the public /rpc/_config endpoint is open.
func (g *Gateway) ConfigExposed() bool { return g.configExposed }

// ConfigReportBody builds the sanitized runtime-config report. Exported so an
// operator UI (e.g. the explorer) can render it in-process without opening the
// public endpoint — mirroring IntrospectBody.
func (g *Gateway) ConfigReportBody() ConfigReport {
	rep := ConfigReport{
		MaxInFlight:       g.maxInFlight,
		InFlight:          g.inFlight.Load(),
		AdvertiseURL:      g.advertiseURL,
		TrustUpstream:     g.trustUpstreamWired,
		IntrospectExposed: g.introspectExposed,
		AuthConfigured:    g.authBinding != nil,
		AuthzConfigured:   g.authzBinding != nil,
		Plugins:           g.PluginNames(),
		Routers:           g.engine.Routers(),
	}
	if g.breakers != nil {
		rep.RemoteBreaker = BreakerReport{
			Disabled:         g.breakers.disabled,
			FailureThreshold: g.breakers.threshold,
			Cooldown:         g.breakers.cooldown.String(),
		}
	}
	// A non-default ClaimsCache means the operator wired a shared/external
	// cache; report the fact (never the impl) so topology is legible.
	if g.authCache != nil {
		if _, isDefault := g.authCache.(*memClaimsCache); !isDefault {
			rep.CustomClaimsCache = true
		}
	}
	return rep
}

// handleConfig serves /rpc/_config — the sanitized runtime-config dump. Like
// _introspect it returns the report as its own JSON body (an ops/debug surface,
// not a business RPC), so it is not wrapped in the {"data":...} envelope.
func (g *Gateway) handleConfig(_ context.Context) *Response {
	body, err := json.Marshal(g.ConfigReportBody())
	if err != nil {
		return ErrorResponse(&rpc.Error{Status: 500, Code: "INTERNAL", Message: "config marshal failed"})
	}
	return &Response{Status: 200, Body: body}
}
