package requestid_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/requestid"
	"github.com/Toyz/sov/rpc"
)

type EchoRouter struct{ seen string }

func (r *EchoRouter) Ping(ctx *rpc.Context) (string, error) {
	r.seen = requestid.FromContext(ctx)
	return r.seen, nil
}

func TestRequestID_GeneratesWhenMissing(t *testing.T) {
	gw := gateway.New()
	if err := gw.Use(requestid.New(requestid.Config{})); err != nil {
		t.Fatalf("Use: %v", err)
	}
	er := &EchoRouter{}
	gw.Register(er)

	resp := gw.Handle(context.Background(), &gateway.Request{
		Method: http.MethodPost, Path: "/rpc/Echo/ping",
		Header: gateway.Header{}, Body: []byte(`{"args":[]}`),
	})
	if resp.Status != 200 {
		t.Fatalf("status=%d body=%s", resp.Status, resp.Body)
	}
	if er.seen == "" {
		t.Fatal("handler saw empty request id")
	}
	if len(er.seen) != 32 {
		t.Fatalf("default id should be 32-hex, got %q", er.seen)
	}
}

func TestRequestID_PropagatesExisting(t *testing.T) {
	gw := gateway.New()
	if err := gw.Use(requestid.New(requestid.Config{})); err != nil {
		t.Fatalf("Use: %v", err)
	}
	er := &EchoRouter{}
	gw.Register(er)

	resp := gw.Handle(context.Background(), &gateway.Request{
		Method: http.MethodPost, Path: "/rpc/Echo/ping",
		Header: gateway.Header{requestid.Header: "caller-stamped-abc"},
		Body:   []byte(`{"args":[]}`),
	})
	if resp.Status != 200 {
		t.Fatalf("status=%d", resp.Status)
	}
	if er.seen != "caller-stamped-abc" {
		t.Fatalf("expected caller-stamped id, got %q", er.seen)
	}
}

func TestRequestID_CustomGenerator(t *testing.T) {
	gw := gateway.New()
	if err := gw.Use(requestid.New(requestid.Config{Generator: func() string { return "fixed-id" }})); err != nil {
		t.Fatalf("Use: %v", err)
	}
	er := &EchoRouter{}
	gw.Register(er)

	resp := gw.Handle(context.Background(), &gateway.Request{
		Method: http.MethodPost, Path: "/rpc/Echo/ping",
		Header: gateway.Header{}, Body: []byte(`{"args":[]}`),
	})
	if resp.Status != 200 {
		t.Fatalf("status=%d", resp.Status)
	}
	if !strings.Contains(string(resp.Body), "fixed-id") {
		t.Fatalf("custom generator not used, body=%s", resp.Body)
	}
}
