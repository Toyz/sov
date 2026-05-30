package cors_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/cors"
	"github.com/Toyz/sov/gateway/builtin/requestid"
	"github.com/Toyz/sov/rpc"
)

type PingRouter struct{}

func (r *PingRouter) Hello(_ *rpc.Context) (string, error) { return "hi", nil }

func TestCORS_DefaultAllowsAny(t *testing.T) {
	gw := gateway.New()
	if err := gw.Use(requestid.New(requestid.Config{})); err != nil {
		t.Fatalf("Use requestid: %v", err)
	}
	if err := gw.Use(cors.New(cors.Config{})); err != nil {
		t.Fatalf("Use: %v", err)
	}
	gw.Register(&PingRouter{})

	resp := gw.Handle(context.Background(), &gateway.Request{
		Method: http.MethodPost, Path: "/rpc/Ping/hello",
		Header: gateway.Header{"Origin": "https://app.example"},
		Body:   []byte(`{"args":[]}`),
	})
	if resp.Status != 200 {
		t.Fatalf("status=%d body=%s", resp.Status, resp.Body)
	}
	if got := resp.Header["Access-Control-Allow-Origin"]; got != "*" {
		t.Errorf("ACAO=%q want *", got)
	}
}

func TestCORS_RestrictedOrigin(t *testing.T) {
	gw := gateway.New()
	if err := gw.Use(requestid.New(requestid.Config{})); err != nil {
		t.Fatalf("Use requestid: %v", err)
	}
	if err := gw.Use(cors.New(cors.Config{Origins: []string{"https://allowed.example"}})); err != nil {
		t.Fatalf("Use: %v", err)
	}
	gw.Register(&PingRouter{})

	resp := gw.Handle(context.Background(), &gateway.Request{
		Method: http.MethodPost, Path: "/rpc/Ping/hello",
		Header: gateway.Header{"Origin": "https://allowed.example"},
		Body:   []byte(`{"args":[]}`),
	})
	if got := resp.Header["Access-Control-Allow-Origin"]; got != "https://allowed.example" {
		t.Errorf("ACAO=%q", got)
	}

	resp = gw.Handle(context.Background(), &gateway.Request{
		Method: http.MethodPost, Path: "/rpc/Ping/hello",
		Header: gateway.Header{"Origin": "https://attacker.example"},
		Body:   []byte(`{"args":[]}`),
	})
	if _, ok := resp.Header["Access-Control-Allow-Origin"]; ok {
		t.Errorf("disallowed origin should not get ACAO")
	}
}

func TestCORS_PreflightShortCircuits(t *testing.T) {
	gw := gateway.New()
	if err := gw.Use(requestid.New(requestid.Config{})); err != nil {
		t.Fatalf("Use requestid: %v", err)
	}
	if err := gw.Use(cors.New(cors.Config{AllowMethods: []string{"GET", "POST"}, MaxAge: 86400})); err != nil {
		t.Fatalf("Use: %v", err)
	}
	gw.Register(&PingRouter{})

	resp := gw.Handle(context.Background(), &gateway.Request{
		Method: http.MethodOptions, Path: "/rpc/Ping/hello",
		Header: gateway.Header{"Origin": "https://app.example"},
	})
	if resp.Status != 204 {
		t.Fatalf("preflight status=%d", resp.Status)
	}
	if len(resp.Body) != 0 {
		t.Errorf("preflight body should be empty, got %s", resp.Body)
	}
	if got := resp.Header["Access-Control-Allow-Methods"]; got != "GET, POST" {
		t.Errorf("methods=%q", got)
	}
	if got := resp.Header["Access-Control-Max-Age"]; got != "86400" {
		t.Errorf("maxage=%q", got)
	}
}
