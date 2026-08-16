package gatewaytest_test

import (
	"strings"
	"testing"

	"github.com/Toyz/sov/gateway/builtin/rpc"
	"github.com/Toyz/sov/gateway/gatewaytest"
	rpccore "github.com/Toyz/sov/rpc"
)

// EchoRouter embeds rpc.Served so the /rpc surface exposes it.
type EchoRouter struct{ rpc.Served }

type EchoParams struct {
	Msg string `json:"msg"`
}

func (EchoRouter) Say(_ *rpccore.Context, p *EchoParams) (map[string]string, error) {
	return map[string]string{"echo": p.Msg}, nil
}

func TestGatewayTest_CallThroughGateway(t *testing.T) {
	gw := gatewaytest.New()
	gw.Register(&EchoRouter{})

	status, body := gatewaytest.Call(gw, "Echo", "say", EchoParams{Msg: "hi"})
	if status != 200 {
		t.Fatalf("status = %d body=%s", status, body)
	}
	if !strings.Contains(string(body), `"echo":"hi"`) {
		t.Fatalf("body = %s", body)
	}
}
