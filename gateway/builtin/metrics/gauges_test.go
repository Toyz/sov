package metrics_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/metrics"
	"github.com/Toyz/sov/gateway/internal/gwtest"
)

func TestMetrics_RuntimeAndInFlightGauges(t *testing.T) {
	gw := gwtest.New()
	mustUse(t, gw, metrics.New(metrics.Config{}))
	gw.Register(&PingRouter{})

	gw.Handle(context.Background(), &gateway.Request{
		Method: http.MethodPost, Path: "/rpc/Ping/hello", Header: gateway.Header{}, Body: []byte(`{"args":[]}`),
	})

	resp := gw.Handle(context.Background(), &gateway.Request{
		Method: http.MethodGet, Path: "/metrics", Header: gateway.Header{},
	})
	if resp.Status != 200 {
		t.Fatalf("/metrics status = %d", resp.Status)
	}
	body := string(resp.Body)
	for _, want := range []string{
		"go_goroutines ",
		"go_memstats_alloc_bytes ",
		"go_memstats_heap_inuse_bytes ",
		"go_gc_cycles_total ",
		"sov_in_flight_requests ",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("/metrics missing gauge %q\n---\n%s", want, body)
		}
	}
	// The scrape request itself is in flight while rendering, so the gauge is
	// at least 1 and the TYPE line is present.
	if !strings.Contains(body, "# TYPE sov_in_flight_requests gauge") {
		t.Fatalf("in-flight gauge TYPE line missing:\n%s", body)
	}
}
