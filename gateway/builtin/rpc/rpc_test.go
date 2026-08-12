package rpc_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/rpc"
	sovrpc "github.com/Toyz/sov/rpc"
)

// MarkedRouter opts into the RPC surface by embedding rpc.Served.
type MarkedRouter struct{ rpc.Served }

func (MarkedRouter) Ping(_ *sovrpc.Context) (string, error) { return "pong", nil }

// PlainRouter carries no marker.
type PlainRouter struct{}

func (PlainRouter) Ping(_ *sovrpc.Context) (string, error) { return "plain", nil }

func call(gw *gateway.Gateway, path string) *gateway.Response {
	return gw.Handle(context.Background(), &gateway.Request{
		Method: http.MethodPost, Path: path, Header: gateway.Header{}, Body: []byte(`{"args":[]}`),
	})
}

// Default: every registered router is served over /rpc, marker or not (the
// deprecated no-marker flow).
func TestRPC_DefaultServesAll(t *testing.T) {
	gw := gateway.New()
	gw.MustUse(rpc.New())
	gw.Register(&PlainRouter{})
	gw.Register(&MarkedRouter{})

	if resp := call(gw, "/rpc/Plain/ping"); resp.Status != 200 || !strings.Contains(string(resp.Body), "plain") {
		t.Fatalf("plain should serve by default: %d %s", resp.Status, resp.Body)
	}
	if resp := call(gw, "/rpc/Marked/ping"); resp.Status != 200 || !strings.Contains(string(resp.Body), "pong") {
		t.Fatalf("marked should serve: %d %s", resp.Status, resp.Body)
	}
}

// RequireMarker: only routers that embed rpc.Served are served; an unmarked
// local router 404s.
func TestRPC_RequireMarker(t *testing.T) {
	gw := gateway.New()
	gw.MustUse(rpc.New(rpc.Config{RequireMarker: true}))
	gw.Register(&PlainRouter{})
	gw.Register(&MarkedRouter{})

	if resp := call(gw, "/rpc/Marked/ping"); resp.Status != 200 {
		t.Fatalf("marked should serve in strict mode: %d %s", resp.Status, resp.Body)
	}
	if resp := call(gw, "/rpc/Plain/ping"); resp.Status != http.StatusNotFound {
		t.Fatalf("unmarked should 404 in strict mode: %d %s", resp.Status, resp.Body)
	}
}

func tagged(cat *gateway.IntrospectReport, name, surface string) bool {
	rds := cat.Services[name]
	return len(rds) > 0 && rds[0].HasSurface(surface)
}

// The rpc surface tags its served routers with the "rpc" surface in the
// introspect catalog — full marker support, symmetric with mcp. Default tags
// every served router; strict mode tags only rpc.Served routers.
func TestRPC_TagsRPCSurface(t *testing.T) {
	gw := gateway.New()
	gw.MustUse(rpc.New())
	gw.Register(&PlainRouter{})
	gw.Register(&MarkedRouter{})
	cat := gw.FederatedCatalog(context.Background())
	if cat == nil || !tagged(cat, "Plain", "rpc") || !tagged(cat, "Marked", "rpc") {
		t.Fatalf("default: both routers should be tagged rpc: %+v", cat)
	}

	gws := gateway.New()
	gws.MustUse(rpc.New(rpc.Config{RequireMarker: true}))
	gws.Register(&PlainRouter{})
	gws.Register(&MarkedRouter{})
	cs := gws.FederatedCatalog(context.Background())
	if cs == nil || !tagged(cs, "Marked", "rpc") {
		t.Fatalf("strict: marked should be tagged rpc: %+v", cs)
	}
	if tagged(cs, "Plain", "rpc") {
		t.Fatalf("strict: unmarked should NOT be tagged rpc: %+v", cs)
	}
}
