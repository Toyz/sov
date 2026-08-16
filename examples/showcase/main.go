// Package main is a showcase gateway that exercises every sov introspection
// feature so the API explorer (/rpc/_explorer/) has something rich to render:
//
//   - params with title/desc/example docs, maxlen constraints, required flags
//   - positional params, header-bound params (sov:"header=..."), no-arg methods
//   - nested named types, a shared multi-owner type (Money), and type DRIFT
//     (two services return a same-named "Address" with divergent shapes)
//   - pagination (rpc.PageParams -> rpc.Page[T]) and server streaming (rpc.Stream[T])
//   - a method authz requirement (perm sentinel), a deprecated method and a
//     deprecated field, and a soft-internal method (needs the "internal" toggle)
//
// Run it and open the explorer:
//
//	SOV_LISTEN=:8090 go run ./examples/showcase
//	open http://localhost:8090/rpc/_explorer/
package main

import (
	"context"
	"log"
	"os"

	"github.com/Toyz/sov/examples/showcase/accounts"
	"github.com/Toyz/sov/examples/showcase/shipping"
	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/explorer"
	rpcsurface "github.com/Toyz/sov/gateway/builtin/rpc"
)

func main() {
	gw := gateway.New()
	gw.MustUse(rpcsurface.New())
	gw.MustUse(explorer.New(explorer.Config{Public: true})) // no auth in the demo

	gw.Register(&CatalogRouter{})
	gw.Register(&BillingRouter{})
	gw.Register(&TenantRouter{})
	gw.Register(&accounts.AccountsRouter{})
	gw.Register(&shipping.ShippingRouter{})

	addr := os.Getenv("SOV_LISTEN")
	if addr == "" {
		addr = ":8090"
	}
	log.Printf("sov showcase on %s — explorer at http://localhost%s/rpc/_explorer/", addr, addr)
	log.Fatal(gw.ListenAndServe(context.Background(), addr))
}
