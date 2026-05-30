package gateway

import (
	"context"
	"net/http"

	"github.com/Toyz/sov/rpc"
)

// frameworkEndpoint dispatches /rpc/_X paths and the top-level /health
// URL the gateway owns. Returns nil if path does not match a framework
// endpoint, so the caller can fall through to business-service routing.
//
// Exposure rules (see PLAN §"Framework endpoint exposure"):
//
//   - /rpc/_health is ALWAYS on (probe target for upstream gateways).
//   - /rpc/_batch is always on.
//   - /rpc/_register and the public top-level /health are owned by
//     the gateway/builtin/registry plugin via RouteHandler. A bare
//     gateway 404s on those. /rpc/_introspect is always on; the
//     aggregator behavior (fan-out across registered remotes) runs
//     whenever the resolver has remote entries.
func (g *Gateway) frameworkEndpoint(ctx context.Context, req *Request) *Response {
	switch req.Path {
	case "/rpc/_health":
		if req.Method != http.MethodGet && req.Method != http.MethodPost {
			return ErrorResponse(&rpc.Error{Status: 405, Code: "BAD_REQUEST", Message: "method not allowed"})
		}
		return g.handleHealth(ctx)
	case "/rpc/_introspect":
		// Always-on: probe target for upstream aggregators. The handler
		// returns just the local engine's catalog on non-registry
		// gateways; registry gateways fan out across registered remotes
		// and aggregate.
		if req.Method != http.MethodGet && req.Method != http.MethodPost {
			return ErrorResponse(&rpc.Error{Status: 405, Code: "BAD_REQUEST", Message: "method not allowed"})
		}
		return g.handleIntrospect(ctx, req)
	}
	return nil
}
