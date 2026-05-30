// Package manifest emits the PEMM manifest of a running gateway —
// a single JSON document describing services, plugins, role bindings,
// federation map, and registered remotes. Ops consume one URL to see
// the deployment shape.
//
// The plugin owns /rpc/_manifest as a RouteHandler. Response is
// JSON-shaped:
//
//	{
//	  "services": ["Auth", "Authz", "User", ...],
//	  "plugins":  [{"name": "registry", "hooks": [...]}, ...],
//	  "auth":     {"service": "Auth", "method": "verify"},
//	  "authz":    {"service": "Authz", "method": "check"},
//	  "remotes":  {"http://team-feed:9100": ["Chirp", "Feed"]},
//	  "introspectables": ["Auth", "Authz", "Chirp", ...]
//	}
//
//	gw.Use(manifest.New())
package manifest

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/Toyz/sov/gateway"
)

// Config has no fields yet — reserved for future knobs (mount path
// override, redact list, etc.). Empty struct preserves the uniform
// New(Config{}) call shape across all builtins.
type Config struct{}

// Plugin is the manifest emitter returned by New.
type Plugin struct{ gw *gateway.Gateway }

// New returns the manifest plugin from cfg.
func New(cfg Config) *Plugin { _ = cfg; return &Plugin{} }

// Compile-time proof of the hooks this plugin binds — a signature
// drift here is a build error, not a silent non-binding at runtime.
var (
	_ gateway.Plugin        = (*Plugin)(nil)
	_ gateway.PluginDoc     = (*Plugin)(nil)
	_ gateway.ConfigApplier = (*Plugin)(nil)
	_ gateway.RouteHandler  = (*Plugin)(nil)
)

// PluginName surfaces in /rpc/_introspect.plugins[].
func (p *Plugin) PluginName() string { return "manifest" }

// Doc surfaces a one-line description in /rpc/_introspect + the explorer.
func (p *Plugin) Doc() string {
	return "Serves /rpc/_manifest — services, plugins, role bindings, and remotes in one document."
}

// Apply grabs the gateway pointer for later use.
func (p *Plugin) Apply(g *gateway.Gateway) error { p.gw = g; return nil }

// RoutePatterns claims /rpc/_manifest.
func (p *Plugin) RoutePatterns() []string { return []string{"/rpc/_manifest"} }

// ManifestReport is the JSON-marshalled body of /rpc/_manifest.
type ManifestReport struct {
	Services        []string              `json:"services"`
	Plugins         []gateway.PluginInfo  `json:"plugins"`
	Auth            *gateway.AuthBinding  `json:"auth,omitempty"`
	Authz           *gateway.AuthzBinding `json:"authz,omitempty"`
	Remotes         map[string][]string   `json:"remotes,omitempty"`
	Introspectables []string              `json:"introspectables,omitempty"`
}

// ServeRoute builds the manifest report on demand.
func (p *Plugin) ServeRoute(_ context.Context, _ *gateway.Request) *gateway.Response {
	if p.gw == nil {
		return &gateway.Response{Status: 503, Body: []byte(`{"error":"gateway not bound"}`)}
	}
	rpt := ManifestReport{
		Plugins: p.gw.PluginInfos(),
	}
	if res := p.gw.Resolver(); res != nil {
		rpt.Services = res.Services()
		rpt.Introspectables = res.Introspectables()
	}
	if rr := p.gw.RegisterResolver(); rr != nil {
		rpt.Remotes = rr.AddressGroup()
	}
	rpt.Auth = p.gw.AuthBinding()
	rpt.Authz = p.gw.AuthzBinding()
	body, _ := json.Marshal(rpt)
	return &gateway.Response{Status: http.StatusOK, Header: gateway.Header{"Content-Type": "application/json"}, Body: body}
}
