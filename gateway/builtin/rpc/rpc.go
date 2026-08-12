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

// Served is the marker a router EMBEDS to declare it is exposed over the RPC
// surface — the counterpart of mcp.Tool. Embedding it (and only embedding it)
// makes the router satisfy ServedRouter; the marker method is UNEXPORTED, so the
// engine never reflects it as a handler and no code outside this package can
// forge the capability.
//
//	type NotesRouter struct{ rpc.Served }
//	func (NotesRouter) Get(ctx *rpc.Context, p *GetParams) (*Note, error) { ... }
//
// Marking is OPTIONAL today: rpc.New() serves EVERY registered router, marked or
// not. rpc.New(rpc.Config{RequireMarker: true}) serves ONLY marked routers — a
// local router without the marker 404s. The unmarked flow is deprecated in favor
// of the marker; embed rpc.Served on new routers.
type Served struct{}

func (Served) sovRPCServed() {}

// ServedRouter is the capability the strict RPC surface filters for. Satisfied
// only by embedding Served.
type ServedRouter interface{ sovRPCServed() }

// Config configures the RPC surface.
type Config struct {
	// RequireMarker serves ONLY routers that embed rpc.Served: a local router
	// without the marker 404s on /rpc. Default false — serve every registered
	// router (the deprecated no-marker flow). Remote routers are always proxied;
	// their home node enforces its own marker.
	RequireMarker bool
}

// Plugin is the RPC surface — a RouteHandler mounted at /rpc/.
type Plugin struct {
	gw  *gateway.Gateway
	cfg Config
}

var (
	_ gateway.Plugin        = (*Plugin)(nil)
	_ gateway.PluginDoc     = (*Plugin)(nil)
	_ gateway.ConfigApplier = (*Plugin)(nil)
	_ gateway.RouteHandler  = (*Plugin)(nil)
)

// New returns the RPC surface plugin.
//
//	gw.Use(rpc.New())                              // serve all registered routers
//	gw.Use(rpc.New(rpc.Config{RequireMarker: true})) // serve only rpc.Served routers
func New(cfg ...Config) *Plugin {
	var c Config
	if len(cfg) > 0 {
		c = cfg[0]
	}
	return &Plugin{cfg: c}
}

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
	// Strict mode: a LOCAL router must embed rpc.Served. A name not in the local
	// engine is remote (or genuinely unregistered) — let Dispatch proxy or 404.
	if p.cfg.RequireMarker {
		if v, ok := p.gw.Engine().RouterValue(router); ok {
			if _, marked := v.(ServedRouter); !marked {
				return gateway.ErrorResponse(sovrpc.NotFound("router %q is not exposed over rpc (embed rpc.Served)", router))
			}
		}
	}
	return p.gw.Dispatch(ctx, req)
}
