package gateway_test

import (
	. "github.com/Toyz/sov/gateway"

	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Toyz/sov/rpc"
)

type stubAggregator struct {
	calls   atomic.Int32
	addSvcs []string
	failNow bool
}

func (s *stubAggregator) PluginName() string { return "stub-aggregator" }
func (s *stubAggregator) ContributeIntrospect(_ context.Context, report *IntrospectReport, _ string, _ []string) error {
	s.calls.Add(1)
	if s.failNow {
		return rpcErr("aggregator-failed")
	}
	for _, name := range s.addSvcs {
		report.Services[name] = []rpc.RouterDescriptor{{Router: name, Title: name + " (aggregated)"}}
	}
	return nil
}

func TestIntrospectAggregator_MergesIntoLocalReport(t *testing.T) {
	gw := newRegistryGateway()
	gw.Register(&EchoRouter{})
	agg := &stubAggregator{addSvcs: []string{"Phantom", "Specter"}}
	if err := gw.Use(agg); err != nil {
		t.Fatal(err)
	}

	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodGet, Path: "/rpc/_introspect", Header: Header{},
	})
	if resp.Status != 200 {
		t.Fatalf("status=%d", resp.Status)
	}
	if agg.calls.Load() != 1 {
		t.Errorf("aggregator calls=%d want 1", agg.calls.Load())
	}
	// Confirm aggregated services appear in the response.
	body := string(resp.Body)
	for _, want := range []string{"Phantom", "Specter", "Echo"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in introspect body", want)
		}
	}
}

func TestIntrospectAggregator_FailureLeavesLocalReportIntact(t *testing.T) {
	gw := newRegistryGateway()
	gw.Register(&EchoRouter{})
	agg := &stubAggregator{failNow: true}
	if err := gw.Use(agg); err != nil {
		t.Fatal(err)
	}

	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodGet, Path: "/rpc/_introspect", Header: Header{},
	})
	if resp.Status != 200 {
		t.Fatalf("status=%d", resp.Status)
	}
	// Echo (local) must still be present despite aggregator failure.
	if !strings.Contains(string(resp.Body), "Echo") {
		t.Errorf("local Echo missing after aggregator failure")
	}
}
