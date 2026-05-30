package registry_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/registry"
)

func TestRegistry_EnablesRegisterEndpoint(t *testing.T) {
	gw := gateway.New()
	resp := gw.Handle(context.Background(), &gateway.Request{
		Method: http.MethodPost, Path: "/rpc/_register", Header: gateway.Header{},
		Body: []byte(`{"name":"X","address":"http://x:1"}`),
	})
	if resp.Status != 404 {
		t.Fatalf("pre-plugin status=%d", resp.Status)
	}
	if err := gw.Use(registry.New(registry.Config{})); err != nil {
		t.Fatalf("Use Registry: %v", err)
	}
	resp = gw.Handle(context.Background(), &gateway.Request{
		Method: http.MethodPost, Path: "/rpc/_register", Header: gateway.Header{},
		Body: []byte(`{"name":"X","address":"http://x:1"}`),
	})
	if resp.Status != 200 {
		t.Fatalf("post-plugin status=%d body=%s", resp.Status, resp.Body)
	}
}
