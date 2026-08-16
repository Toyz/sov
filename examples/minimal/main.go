// Minimal sov example: one Echo service, one binary, ~10 lines of wiring.
//
//	go run ./examples/minimal
//	curl -s -X POST localhost:8080/rpc/Echo/say -d '{"args":[{"msg":"hi"}]}'
//
// Then open the API explorer in a browser:
//
//	http://localhost:8080/rpc/_explorer/
package main

import (
	"context"
	"log"
	"os"

	"github.com/Toyz/sov"
	"github.com/Toyz/sov/gateway/builtin/explorer"
	"github.com/Toyz/sov/gateway/builtin/rpc"
)

// EchoRouter embeds rpc.Served — the marker that exposes it over /rpc.
type EchoRouter struct{ rpc.Served }
type SayParams struct {
	Msg string `json:"msg"`
}

func (r *EchoRouter) Say(_ *sov.Context, p *SayParams) (map[string]string, error) {
	if p.Msg == "" {
		return nil, sov.BadRequest("msg required")
	}
	return map[string]string{"echoed": p.Msg}, nil
}

func main() {
	gw := sov.New()
	gw.MustUse(rpc.New()) // the /rpc surface is a builtin — register it like any other
	// The API explorer UI at /rpc/_explorer/. Public here because this demo has
	// no auth; on a real gateway leave Public off so it inherits the auth gate.
	gw.MustUse(explorer.New(explorer.Config{Public: true}))
	gw.Register(&EchoRouter{})
	addr := os.Getenv("SOV_LISTEN")
	if addr == "" {
		addr = ":8080"
	}
	log.Printf("sov minimal on %s — explorer at http://localhost%s/rpc/_explorer/", addr, addr)
	log.Fatal(gw.ListenAndServe(context.Background(), addr))
}
