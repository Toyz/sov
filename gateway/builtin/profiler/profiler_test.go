package profiler_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	. "github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/profiler"
	"github.com/Toyz/sov/gateway/internal/gwtest"
	"github.com/Toyz/sov/rpc"
)

func TestProfiler_ServesIndex(t *testing.T) {
	gw := gwtest.New()
	gw.MustUse(profiler.New())
	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodGet, Path: "/debug/pprof/", Header: Header{},
	})
	if resp.Status != 200 {
		t.Fatalf("pprof index status = %d body=%s", resp.Status, resp.Body)
	}
	// The index lists the standard profiles.
	if !strings.Contains(string(resp.Body), "goroutine") {
		t.Fatalf("pprof index did not list profiles: %s", resp.Body)
	}
}

func TestProfiler_NamedProfileWithQuery(t *testing.T) {
	gw := gwtest.New()
	gw.MustUse(profiler.New())
	// RawQuery must reach pprof (debug=1 → text format).
	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodGet, Path: "/debug/pprof/goroutine", RawQuery: "debug=1", Header: Header{},
	})
	if resp.Status != 200 {
		t.Fatalf("goroutine profile status = %d body=%s", resp.Status, resp.Body)
	}
	if !strings.Contains(string(resp.Body), "goroutine profile") {
		t.Fatalf("unexpected goroutine profile body: %.80s", resp.Body)
	}
}

func TestProfiler_GatedOnAuthedGateway(t *testing.T) {
	gw := gwtest.New()
	gw.RegisterAuth(&authRouter{})
	gw.MustUse(profiler.New())
	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodGet, Path: "/debug/pprof/", Header: Header{},
	})
	if resp.Status != http.StatusUnauthorized {
		t.Fatalf("anon pprof on an authed gateway must be 401, got %d", resp.Status)
	}
}

func TestProfiler_PublicOptIn(t *testing.T) {
	gw := gwtest.New()
	gw.RegisterAuth(&authRouter{})
	gw.MustUse(profiler.New(profiler.Config{Public: true}))
	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodGet, Path: "/debug/pprof/", Header: Header{},
	})
	if resp.Status != 200 {
		t.Fatalf("Public profiler should serve anon, got %d", resp.Status)
	}
}

// authRouter is a minimal AuthService so the gateway has an auth binding
// (which is what gateAnon checks). Verify is never reached by these tests.
type authRouter struct{}

func (r *authRouter) Verify(_ *rpc.Context, _ *VerifyParams) (*Claims, error) {
	return &Claims{Subject: "x"}, nil
}
