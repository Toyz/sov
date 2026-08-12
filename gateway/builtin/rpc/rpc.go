// Package rpc is the RPC surface builtin — the `/rpc/{router}/{method}` wire,
// exactly as gateway/builtin/mcp is the MCP surface. Both are RouteHandler
// plugins that serve a surface over the gateway's Dispatch mesh fabric; neither
// is hardcoded in core. You register it and the gateway routes /rpc to it:
//
//	gw.Use(rpc.New())   // serve /rpc/{router}/{method}
//
// A gateway with no rpc builtin simply doesn't speak the RPC surface — the
// Dispatch fabric still serves other surfaces (MCP) over the same routers. The
// presets (NewMonolith/NewPod/NewRegistry/NewHybrid) and the sov facade register
// it for you.
package rpc

import (
	"context"
	"net/http"

	"github.com/Toyz/sov/gateway"
	sovrpc "github.com/Toyz/sov/rpc"
)

// Config configures the RPC surface. Empty today; present for symmetry with the
// other surface builtins and forward room (custom wire, path, ...).
type Config struct{}

// Plugin is the RPC surface — a RouteHandler mounted at /rpc/.
type Plugin struct{ gw *gateway.Gateway }

var (
	_ gateway.Plugin        = (*Plugin)(nil)
	_ gateway.PluginDoc     = (*Plugin)(nil)
	_ gateway.ConfigApplier = (*Plugin)(nil)
	_ gateway.RouteHandler  = (*Plugin)(nil)
)

// New returns the RPC surface plugin.
//
//	gw.Use(rpc.New())
func New(_ ...Config) *Plugin { return &Plugin{} }

func (p *Plugin) PluginName() string { return "rpc" }

func (p *Plugin) Doc() string {
	return "RPC surface — serves /rpc/{router}/{method} over the Dispatch mesh fabric (local, peer, or remote), the same fabric mcp serves tools over."
}

// Apply captures the gateway so ServeRoute can reach the Dispatch fabric.
func (p *Plugin) Apply(g *gateway.Gateway) error { p.gw = g; return nil }

// RoutePatterns claims the /rpc/ subtree. Framework endpoints (/rpc/_health,
// _introspect, _batch, _register) are handled by core before plugin routes, and
// more-specific plugin routes (/rpc/_explorer/) win by longest-match — so this
// broad claim only receives genuine /rpc/{router}/{method} business calls.
func (p *Plugin) RoutePatterns() []string { return []string{"/rpc/"} }

// ServeRoute is the /rpc surface handler: enforce POST + the reserved-name
// policy, then hand the call to the Dispatch fabric, which resolves it local, to
// an in-process peer, or to a remote node.
func (p *Plugin) ServeRoute(ctx context.Context, req *gateway.Request) *gateway.Response {
	if req.Method != "" && req.Method != http.MethodPost {
		return gateway.ErrorResponse(&sovrpc.Error{Status: 405, Code: "BAD_REQUEST", Message: "method not allowed"})
	}
	router, method, ok := sovrpc.SplitRPCPath(req.Path)
	if !ok {
		return gateway.ErrorResponse(sovrpc.NotFound("path must be /rpc/{router}/{method}"))
	}
	if len(router) > 0 && router[0] == '_' {
		return gateway.ErrorResponse(sovrpc.NotFound("router %q reserved", router))
	}
	if len(method) > 0 && method[0] == '_' {
		return gateway.ErrorResponse(sovrpc.NotFound("method %q is internal-network only", method))
	}
	return p.gw.Dispatch(ctx, req)
}
