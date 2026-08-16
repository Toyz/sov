// Package gatewaytest is the integration-test harness for a sov Gateway — the
// gateway-level analog of rpctest (which dispatches straight into the engine).
// Stand up a gateway with the /rpc surface, register your routers (and remotes
// via gw.RegisterRemote), and Call them through the WHOLE gateway path — auth +
// authz middleware, surfaces, plugins, remote proxying — with no HTTP server.
//
//	gw := gatewaytest.New()
//	gw.Register(&MyRouter{})
//	status, body := gatewaytest.Call(gw, "My", "thing", MyParams{...})
//
// For a mesh-routing test, register a remote and call across the hop:
//
//	gw.RegisterRemote("Widgets", upstream.URL, time.Minute)
//	status, body := gatewaytest.Call(gw, "Widgets", "create", p,
//	    gatewaytest.WithBearer(token))
package gatewaytest

import (
	"context"
	"encoding/json"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/rpc"
)

// New builds a gateway with the /rpc surface registered — the common setup a
// real monolith/pod uses. Pass any gateway.Option (WithAdvertiseURL, auth
// wiring, plugins) exactly as in production.
func New(opts ...gateway.Option) *gateway.Gateway {
	gw := gateway.New(opts...)
	gw.MustUse(rpc.New())
	return gw
}

// WithBearer returns a Header carrying an Authorization: Bearer token, for
// passing to Call on an authed gateway.
func WithBearer(token string) gateway.Header {
	return gateway.Header{"Authorization": "Bearer " + token}
}

// Call dispatches POST /rpc/{router}/{method} through the full gateway and
// returns the HTTP status + response body. params is marshalled into the sov
// envelope {"args":[params]}. Extra headers (e.g. WithBearer) are merged in.
// Unlike rpctest.CallInto (engine only), this exercises the surfaces, the
// auth/authz middleware, and remote proxying — the real request path.
func Call(gw *gateway.Gateway, router, method string, params any, headers ...gateway.Header) (int, []byte) {
	body, _ := json.Marshal(map[string]any{"args": []any{params}})
	h := gateway.Header{"Content-Type": "application/json"}
	for _, extra := range headers {
		for k, v := range extra {
			h[k] = v
		}
	}
	resp := gw.Handle(context.Background(), &gateway.Request{
		Method: "POST",
		Path:   "/rpc/" + router + "/" + method,
		Header: h,
		Body:   body,
	})
	return resp.Status, resp.Body
}
