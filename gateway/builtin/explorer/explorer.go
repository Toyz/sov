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
}

// Plugin is the explorer-UI route owner returned by New.
type Plugin struct {
	gw     *gateway.Gateway
	prefix string
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
func New(cfg Config) *Plugin {
	prefix := cfg.PathPrefix
	if prefix == "" {
		prefix = "/rpc/_explorer"
	}
	return &Plugin{prefix: prefix}
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
	// The "show internal" toggle fetches a distinct path; translate it to
	// the introspect header so the gateway returns the full payload
	// (soft-hidden methods included). Request.Path carries no query string,
	// hence a path variant rather than ?internal=1.
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
