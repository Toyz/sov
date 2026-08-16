// Package explorer mounts the embedded HTML browser at
// /rpc/_explorer/. Default off; production binaries opt-in
// explicitly. The plugin OWNS the route — it registers
// /rpc/_explorer/ as a RouteHandler subtree match and renders by
// re-entering the gateway on /rpc/_introspect to pick up the catalog.
// The framework holds zero explorer state.
//
//	gw.Use(explorer.New())
package explorer

import (
	"context"
	"net/http"
	"strings"

	"github.com/Toyz/sov/gateway"
)

// Config configures the explorer plugin. PathPrefix overrides the
// default "/rpc/_explorer" mount path (leading slash required;
// trailing slash added automatically to the subtree match).
type Config struct {
	PathPrefix string
	// Public serves the explorer to ANONYMOUS callers even when the gateway
	// has auth configured. Default false: a gateway with an auth binding
	// requires a valid subject to reach the explorer (it discloses the full
	// internal catalog, including soft-hidden methods). A gateway with no auth
	// (local/dev) is always open. Set true to intentionally expose the UI
	// publicly on an authed gateway.
	Public bool
}

// Plugin is the explorer-UI route owner returned by New.
type Plugin struct {
	gw     *gateway.Gateway
	prefix string
	public bool
}

// Compile-time proof of the hooks this plugin binds — a signature
// drift here is a build error, not a silent non-binding at runtime.
var (
	_ gateway.Plugin        = (*Plugin)(nil)
	_ gateway.PluginDoc     = (*Plugin)(nil)
	_ gateway.ConfigApplier = (*Plugin)(nil)
	_ gateway.RouteHandler  = (*Plugin)(nil)
)

// New returns an explorer plugin from cfg.
func New(cfgs ...Config) *Plugin {
	if len(cfgs) > 1 {
		panic("explorer.New: at most one Config")
	}
	var cfg Config
	if len(cfgs) == 1 {
		cfg = cfgs[0]
	}
	prefix := cfg.PathPrefix
	if prefix == "" {
		prefix = "/rpc/_explorer"
	}
	return &Plugin{prefix: prefix, public: cfg.Public}
}

// gateAnon returns a 401 for an anonymous caller when the gateway has auth
// configured and Public was not set — so a disclosure endpoint isn't wide open
// on a production gateway. Returns nil (serve) for a Public plugin or a gateway
// with no auth binding (local/dev), or when the caller is authenticated.
func (p *Plugin) gateAnon(req *gateway.Request) *gateway.Response {
	if p.public || p.gw == nil || p.gw.AuthBinding() == nil || req.User != nil {
		return nil
	}
	return &gateway.Response{
		Status: http.StatusUnauthorized,
		Header: gateway.Header{"Content-Type": "application/json"},
		Body:   []byte(`{"error":{"code":"UNAUTHORIZED","message":"authentication required for the explorer (set explorer Public:true to allow anonymous access)"}}`),
	}
}

// PluginName surfaces in /rpc/_introspect.plugins[].
func (p *Plugin) PluginName() string { return "explorer" }

// Doc surfaces a one-line description in /rpc/_introspect + the explorer.
func (p *Plugin) Doc() string {
	return "Serves the interactive API explorer UI at /rpc/_explorer/."
}

// Apply grabs the gateway pointer for later introspect re-entry.
func (p *Plugin) Apply(g *gateway.Gateway) error { p.gw = g; return nil }

// RoutePatterns claims the explorer subtree. Trailing slash → subtree
// (prefix) match per net/http ServeMux convention.
func (p *Plugin) RoutePatterns() []string {
	return []string{p.prefix, p.prefix + "/"}
}

// ServeRoute renders the embedded UI. The catalog is whatever the
// gateway's own introspect report produces (registry-mode or pod-mode —
// same surface). It calls IntrospectBody directly rather than re-entering
// the /rpc/_introspect endpoint, so the explorer works even when that
// endpoint is opt-in-disabled (it discloses the same surface the explorer
// renders, so coupling them would force the endpoint open).
func (p *Plugin) ServeRoute(ctx context.Context, req *gateway.Request) *gateway.Response {
	if resp := p.gateAnon(req); resp != nil {
		return resp
	}
	// The "show internal" toggle fetches a distinct path; translate it to
	// the introspect header so the gateway returns the full payload
	// (soft-hidden methods included). Request.Path carries no query string,
	// hence a path variant rather than ?internal=1.
	// Extension surface: the manifest of plugin-provided assets, and the assets
	// themselves when a plugin handed us their bytes (ExplorerExtender). Kept off
	// the catalog path so a plugin needs no route of its own.
	if strings.HasSuffix(req.Path, "/extensions.json") {
		return &gateway.Response{
			Status: http.StatusOK,
			Header: gateway.Header{"Content-Type": "application/json"},
			Body:   explorerManifest(p.prefix, p.collectAssets()),
		}
	}
	if strings.HasPrefix(req.Path, p.prefix+"/ext/") {
		ct, body, status := serveExtAsset(req.Path, p.prefix, p.collectAssets())
		return &gateway.Response{Status: status, Header: gateway.Header{"Content-Type": ct}, Body: body}
	}

	header := gateway.Header{}
	if strings.HasSuffix(req.Path, "/api-internal.json") {
		header[gateway.IntrospectInternalHeader] = "1"
	}
	introResp := p.gw.IntrospectBody(ctx, &gateway.Request{
		Method: http.MethodGet,
		Path:   "/rpc/_introspect",
		Header: header,
	})
	ct, body, status := serveUI(req.Path, p.prefix, introResp.Body)
	return &gateway.Response{
		Status: status,
		Header: gateway.Header{"Content-Type": ct},
		Body:   body,
	}
}
