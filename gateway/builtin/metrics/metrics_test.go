package metrics_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/metrics"
	"github.com/Toyz/sov/gateway/builtin/requestid"
	"github.com/Toyz/sov/rpc"
)

type PingRouter struct{}

func (*PingRouter) Hello(*rpc.Context) (string, error) { return "hi", nil }

func mustUse(t *testing.T, gw *gateway.Gateway, plugins ...any) {
	t.Helper()
	for _, p := range plugins {
		if err := gw.Use(p); err != nil {
			t.Fatalf("Use(%T): %v", p, err)
		}
	}
}

func TestMetrics_RequiresRequestID(t *testing.T) {
	gw := gateway.New()
	if err := gw.Use(metrics.New(metrics.Config{})); err != nil {
		t.Fatalf("Use accepts plugins in any order: %v", err)
	}
	// Requires validation now deferred to ListenAndServe — boot
	// fails when request-id is still missing.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := gw.ListenAndServe(ctx, ":0")
	if err == nil || !strings.Contains(err.Error(), "request-id") {
		t.Fatalf("expected boot to fail referencing request-id, got %v", err)
	}
}

func TestMetrics_DispatchAndExpose(t *testing.T) {
	gw := gateway.New()
	mustUse(t, gw,
		requestid.New(requestid.Config{}),
		metrics.New(metrics.Config{}),
	)
	gw.Register(&PingRouter{})

	for i := 0; i < 3; i++ {
		resp := gw.Handle(context.Background(), &gateway.Request{
			Method: http.MethodPost, Path: "/rpc/Ping/hello",
			Header: gateway.Header{}, Body: []byte(`{"args":[]}`),
		})
		if resp.Status != 200 {
			t.Fatalf("iter=%d status=%d", i, resp.Status)
		}
	}

	scrape := gw.Handle(context.Background(), &gateway.Request{
		Method: http.MethodGet, Path: "/metrics",
		Header: gateway.Header{},
	})
	if scrape.Status != 200 {
		t.Fatalf("scrape status=%d", scrape.Status)
	}
	body := string(scrape.Body)
	if !strings.Contains(body, "sov_requests_total{") {
		t.Errorf("missing counter line; body=\n%s", body)
	}
	if !strings.Contains(body, `router="Ping"`) {
		t.Errorf("missing router label; body=\n%s", body)
	}
	if !strings.Contains(body, `method="hello"`) {
		t.Errorf("missing method label; body=\n%s", body)
	}
	if !strings.Contains(body, "sov_request_duration_seconds_bucket{") {
		t.Errorf("missing histogram bucket; body=\n%s", body)
	}
	if !strings.Contains(body, ` 3\n`) && !strings.Contains(body, " 3\n") {
		t.Errorf("expected count=3 somewhere; body=\n%s", body)
	}
}

func TestMetrics_SnapshotCapability(t *testing.T) {
	gw := gateway.New()
	mustUse(t, gw,
		requestid.New(requestid.Config{}),
		metrics.New(metrics.Config{}),
	)
	gw.Register(&PingRouter{})

	for i := 0; i < 5; i++ {
		gw.Handle(context.Background(), &gateway.Request{
			Method: http.MethodPost, Path: "/rpc/Ping/hello",
			Header: gateway.Header{}, Body: []byte(`{"args":[]}`),
		})
	}

	snap, ok := gateway.GetCapability[metrics.Snapshot](gw, "metrics.Snapshot")
	if !ok {
		t.Fatal("metrics.Snapshot capability not published")
	}
	s := snap()
	if s == nil || len(s.Counters) == 0 {
		t.Fatal("empty snapshot")
	}
	var total uint64
	for _, c := range s.Counters {
		total += c
	}
	if total != 5 {
		t.Errorf("snapshot total=%d want 5", total)
	}
	if time.Since(s.At) > time.Second {
		t.Errorf("snapshot At is stale: %v", s.At)
	}
}

func TestMetrics_IntrospectAugment(t *testing.T) {
	gw := gateway.New()
	mustUse(t, gw,
		requestid.New(requestid.Config{}),
		metrics.New(metrics.Config{}),
	)
	gw.Register(&PingRouter{})

	gw.Handle(context.Background(), &gateway.Request{
		Method: http.MethodPost, Path: "/rpc/Ping/hello",
		Header: gateway.Header{}, Body: []byte(`{"args":[]}`),
	})

	intro := gw.Handle(context.Background(), &gateway.Request{
		Method: http.MethodGet, Path: "/rpc/_introspect",
		Header: gateway.Header{},
	})
	if intro.Status != 200 {
		t.Fatalf("introspect status=%d", intro.Status)
	}
	if !strings.Contains(string(intro.Body), `"requests_total":1`) {
		t.Errorf("introspect missing requests_total=1; body=%s", intro.Body)
	}
}
