package gateway_test

import (
	. "github.com/Toyz/sov/gateway"

	"context"
	"net/http"
	"strings"
	"testing"
)

// stubHealthAggregator adds a per-service entry with a configurable
// status.
type stubHealthAggregator struct {
	svcName string
	status  string
}

func (s stubHealthAggregator) PluginName() string { return "stub-health-agg" }
func (s stubHealthAggregator) AggregateHealth(_ context.Context, report *HealthReport) error {
	report.Services[s.svcName] = HealthService{
		Status: s.status,
		Source: "stub://" + s.svcName,
	}
	return nil
}

func TestHealthAggregator_UnhealthyRemoteDowngradesTopLevel(t *testing.T) {
	gw := newRegistryGateway()
	// No local routers — only the aggregator's unhealthy entry exists,
	// so overallStatus("all bad") triggers 503.
	if err := gw.Use(stubHealthAggregator{svcName: "Sad", status: "unhealthy"}); err != nil {
		t.Fatal(err)
	}
	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodGet, Path: "/rpc/_health", Header: Header{},
	})
	if resp.Status != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", resp.Status)
	}
	if !strings.Contains(string(resp.Body), `"status":"unhealthy"`) {
		t.Errorf("body missing top-level unhealthy: %s", resp.Body)
	}
}

func TestHealthAggregator_MixedHealthyUnhealthyReturns207(t *testing.T) {
	// One local healthy + one aggregator-added unhealthy → overall
	// degraded (any-bad-but-not-all-bad), HTTP 207.
	gw := newRegistryGateway()
	gw.Register(&EchoRouter{})
	if err := gw.Use(stubHealthAggregator{svcName: "Sad", status: "unhealthy"}); err != nil {
		t.Fatal(err)
	}
	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodGet, Path: "/rpc/_health", Header: Header{},
	})
	if resp.Status != http.StatusMultiStatus {
		t.Fatalf("status=%d want 207", resp.Status)
	}
	if !strings.Contains(string(resp.Body), `"status":"degraded"`) {
		t.Errorf("body missing top-level degraded: %s", resp.Body)
	}
}

func TestHealthAggregator_HealthyRemoteStaysHealthy(t *testing.T) {
	gw := newRegistryGateway()
	gw.Register(&EchoRouter{})
	if err := gw.Use(stubHealthAggregator{svcName: "Fine", status: "healthy"}); err != nil {
		t.Fatal(err)
	}
	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodGet, Path: "/rpc/_health", Header: Header{},
	})
	if resp.Status != 200 {
		t.Fatalf("status=%d", resp.Status)
	}
}
