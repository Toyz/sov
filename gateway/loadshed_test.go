package gateway_test

import (
	"context"
	"strings"
	"testing"
	"time"

	. "github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/rpc"
	"github.com/Toyz/sov/gateway/internal/gwtest"
	rpccore "github.com/Toyz/sov/rpc"
)

type BlockRouter struct {
	rpc.Served
	enter   chan struct{}
	release chan struct{}
}
type waitParams struct {
	N int `json:"n"`
}

func (r *BlockRouter) Wait(_ *rpccore.Context, _ *waitParams) (map[string]bool, error) {
	r.enter <- struct{}{}
	<-r.release
	return map[string]bool{"ok": true}, nil
}

func TestGateway_LoadShedsAboveMaxInFlight(t *testing.T) {
	r := &BlockRouter{enter: make(chan struct{}, 1), release: make(chan struct{})}
	gw := gwtest.New(WithMaxInFlight(1))
	gw.Register(r)
	defer close(r.release)

	call := func() *Response {
		return gw.Handle(context.Background(), &Request{
			Method: "POST", Path: "/rpc/Block/wait", Header: Header{}, Body: []byte(`{"args":[{}]}`),
		})
	}

	go call() // occupies the only slot, then blocks on release
	select {
	case <-r.enter:
	case <-time.After(3 * time.Second):
		t.Fatal("request 1 never reached the handler")
	}

	resp := call() // in-flight would be 2 > 1 → shed
	if resp.Status != 503 {
		t.Fatalf("expected 503 OVERLOADED, got %d body=%s", resp.Status, resp.Body)
	}
	if !strings.Contains(string(resp.Body), "OVERLOADED") {
		t.Fatalf("body = %s", resp.Body)
	}
}
