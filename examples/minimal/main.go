// Minimal sov example: one Echo service, one binary, ~10 lines of wiring.
//
//	go run ./examples/minimal
//	curl -s -X POST localhost:8080/rpc/Echo/say -d '{"args":[{"msg":"hi"}]}'
package main

import (
	"context"
	"log"

	"github.com/Toyz/sov"
)

type EchoRouter struct{}
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
	gw.Register(&EchoRouter{})
	log.Fatal(gw.ListenAndServe(context.Background(), ":8080"))
}
