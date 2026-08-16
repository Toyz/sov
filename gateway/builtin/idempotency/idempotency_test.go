package idempotency_test

import (
	"testing"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/idempotency"
	"github.com/Toyz/sov/gateway/builtin/rpc"
	"github.com/Toyz/sov/gateway/gatewaytest"
	rpccore "github.com/Toyz/sov/rpc"
)

type CountRouter struct {
	rpc.Served
	calls int
}

type BumpParams struct {
	N int `json:"n"`
}

func (r *CountRouter) Bump(_ *rpccore.Context, _ *BumpParams) (map[string]int, error) {
	r.calls++
	return map[string]int{"calls": r.calls}, nil
}

func TestIdempotency_ReplaysSameKey(t *testing.T) {
	gw := gatewaytest.New()
	gw.MustUse(idempotency.New())
	r := &CountRouter{}
	gw.Register(r)

	key := gateway.Header{"Idempotency-Key": "abc"}
	if s, _ := gatewaytest.Call(gw, "Count", "bump", BumpParams{}, key); s != 200 {
		t.Fatalf("first call status %d", s)
	}
	if s, _ := gatewaytest.Call(gw, "Count", "bump", BumpParams{}, key); s != 200 {
		t.Fatalf("replay status %d", s)
	}
	if r.calls != 1 {
		t.Fatalf("handler ran %d times for a repeated key, want 1 (replayed)", r.calls)
	}

	// A different key executes again.
	gatewaytest.Call(gw, "Count", "bump", BumpParams{}, gateway.Header{"Idempotency-Key": "xyz"})
	if r.calls != 2 {
		t.Fatalf("a different key must execute: calls=%d", r.calls)
	}

	// No key: always executes.
	gatewaytest.Call(gw, "Count", "bump", BumpParams{})
	gatewaytest.Call(gw, "Count", "bump", BumpParams{})
	if r.calls != 4 {
		t.Fatalf("unkeyed calls must always execute: calls=%d", r.calls)
	}
}
