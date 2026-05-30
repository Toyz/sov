package introspect_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/introspect"
	"github.com/Toyz/sov/rpc"
)

type PingRouter struct{}

func (PingRouter) Ping(ctx *rpc.Context) (string, error) { return "pong", nil }

func get(gw *gateway.Gateway, path string) *gateway.Response {
	return gw.Handle(context.Background(), &gateway.Request{
		Method: http.MethodGet, Path: path, Header: gateway.Header{},
	})
}

// The endpoint is OFF until the plugin is used: a closed gateway returns a
// clean 404 (absent), NOT 405 (exists-wrong-method) — a closed endpoint must
// not advertise its existence.
func TestIntrospectClosedByDefault(t *testing.T) {
	gw := gateway.New()
	gw.Register(&PingRouter{})

	resp := get(gw, "/rpc/_introspect")
	if resp.Status != 404 {
		t.Fatalf("default /rpc/_introspect status=%d, want 404 (opt-in, closed); body=%s", resp.Status, resp.Body)
	}
	if strings.Contains(string(resp.Body), "services") {
		t.Fatalf("closed introspect leaked a catalog: %s", resp.Body)
	}
}

// Using the plugin opens the endpoint and serves the catalog.
func TestIntrospectOpensWithPlugin(t *testing.T) {
	gw := gateway.New()
	gw.Register(&PingRouter{})
	gw.MustUse(introspect.New())

	if !gw.IntrospectExposed() {
		t.Fatal("IntrospectExposed() false after Use(introspect.New())")
	}
	resp := get(gw, "/rpc/_introspect")
	if resp.Status != 200 {
		t.Fatalf("opened /rpc/_introspect status=%d, want 200; body=%s", resp.Status, resp.Body)
	}
	if !strings.Contains(string(resp.Body), `"services"`) || !strings.Contains(string(resp.Body), "Ping") {
		t.Fatalf("introspect body not the catalog: %s", resp.Body)
	}
}
