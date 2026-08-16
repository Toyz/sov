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
	"runtime/debug"

	"github.com/Toyz/sov/gateway"
)

// Config configures the manifest plugin.
type Config struct {
	// Public serves the manifest (service list, plugins, role bindings, and
	// internal mesh addresses) to ANONYMOUS callers even when the gateway has
	// auth configured. Default false: an authed gateway requires a valid
	// subject; a no-auth (local/dev) gateway is always open.
	Public bool
}

// Plugin is the manifest emitter returned by New.
type Plugin struct {
	gw     *gateway.Gateway
	public bool
}

// New returns the manifest plugin from cfg.
func New(cfgs ...Config) *Plugin {
	if len(cfgs) > 1 {
		panic("manifest.New: at most one Config")
	}
	var cfg Config
	if len(cfgs) == 1 {
		cfg = cfgs[0]
	}
	return &Plugin{public: cfg.Public}
}

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

// Report is the JSON-marshalled body of /rpc/_manifest.
type Report struct {
	Services        []string              `json:"services"`
	Plugins         []gateway.PluginInfo  `json:"plugins"`
	Auth            *gateway.AuthBinding  `json:"auth,omitempty"`
	Authz           *gateway.AuthzBinding `json:"authz,omitempty"`
	Remotes         map[string][]string   `json:"remotes,omitempty"`
	Introspectables []string              `json:"introspectables,omitempty"`
	Build           *BuildInfo            `json:"build,omitempty"`
}

// BuildInfo is the running binary's build provenance, read from the Go build
// info the linker stamps. Lets ops answer "what revision is this pod" from the
// wire — for deploy/rollback tracking and debugging a heterogeneous mesh.
type BuildInfo struct {
	GoVersion string `json:"go_version,omitempty"`
	Path      string `json:"path,omitempty"`     // main module path
	Version   string `json:"version,omitempty"`  // main module version ((devel) for local builds)
	Revision  string `json:"revision,omitempty"` // vcs.revision
	Time      string `json:"time,omitempty"`     // vcs.time
	Modified  bool   `json:"modified,omitempty"` // vcs.modified — built from a dirty tree
}

// readBuildInfo reads the importing binary's build info. Returns nil when it is
// unavailable (e.g. `go run` without stamping).
func readBuildInfo() *BuildInfo {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return nil
	}
	out := &BuildInfo{GoVersion: bi.GoVersion, Path: bi.Main.Path, Version: bi.Main.Version}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			out.Revision = s.Value
		case "vcs.time":
			out.Time = s.Value
		case "vcs.modified":
			out.Modified = s.Value == "true"
		}
	}
	return out
}

// ServeRoute builds the manifest report on demand.
func (p *Plugin) ServeRoute(_ context.Context, req *gateway.Request) *gateway.Response {
	if p.gw == nil {
		return &gateway.Response{Status: 503, Body: []byte(`{"error":"gateway not bound"}`)}
	}
	// Anonymous callers are blocked on an authed gateway unless Public — the
	// manifest discloses internal mesh addresses + the full plugin list.
	if !p.public && p.gw.AuthBinding() != nil && req.User == nil {
		return &gateway.Response{
			Status: http.StatusUnauthorized,
			Header: gateway.Header{"Content-Type": "application/json"},
			Body:   []byte(`{"error":{"code":"UNAUTHORIZED","message":"authentication required for the manifest (set manifest Public:true to allow anonymous access)"}}`),
		}
	}
	rpt := Report{
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
	rpt.Build = readBuildInfo()
	body, _ := json.Marshal(rpt)
	return &gateway.Response{Status: http.StatusOK, Header: gateway.Header{"Content-Type": "application/json"}, Body: body}
}
