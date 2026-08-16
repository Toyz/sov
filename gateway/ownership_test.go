package gateway_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	. "github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/internal/gwtest"
	"github.com/Toyz/sov/rpc"
)

// Widget is a named entity type. WidgetMaker returns it (producer);
// WidgetReader takes it as input (consumer).
type Widget struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type WidgetMakerRouter struct{}

type MakeParams struct {
	Name string `json:"name"`
}

func (r *WidgetMakerRouter) Make(_ *rpc.Context, p *MakeParams) (*Widget, error) {
	return &Widget{ID: "w1", Name: p.Name}, nil
}

type WidgetReaderRouter struct{}

type ReadParams struct {
	W Widget `json:"w"`
}

func (r *WidgetReaderRouter) Read(_ *rpc.Context, p *ReadParams) (map[string]string, error) {
	return map[string]string{"name": p.W.Name}, nil
}

// SecondMakerRouter ALSO returns Widget with the SAME shape — a shared model,
// not a smell.
type SecondMakerRouter struct{}

func (r *SecondMakerRouter) Make(_ *rpc.Context, p *MakeParams) (*Widget, error) {
	return &Widget{ID: "w2", Name: p.Name}, nil
}

func introspect(t *testing.T, gw *Gateway) *IntrospectReport {
	t.Helper()
	gw.ExposeIntrospect() // endpoint is opt-in; these tests assert its body
	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodGet, Path: "/rpc/_introspect", Header: Header{},
	})
	if resp.Status != 200 {
		t.Fatalf("introspect status=%d", resp.Status)
	}
	var rpt IntrospectReport
	if err := json.Unmarshal(resp.Body, &rpt); err != nil {
		t.Fatalf("decode introspect: %v", err)
	}
	return &rpt
}

func TestOwnership_ProducerOwnsConsumerConsumes(t *testing.T) {
	gw := gwtest.New()
	gw.Register(&WidgetMakerRouter{})
	gw.Register(&WidgetReaderRouter{})

	rpt := introspect(t, gw)
	td, ok := rpt.Types["Widget"]
	if !ok {
		t.Fatalf("Widget type absent from catalog; types=%v", keysOf(rpt.Types))
	}
	if td.Owner != "WidgetMaker" {
		t.Errorf("Widget.Owner = %q, want WidgetMaker", td.Owner)
	}
	if len(td.Owners) != 1 || td.Owners[0] != "WidgetMaker" {
		t.Errorf("Widget.Owners = %v, want [WidgetMaker]", td.Owners)
	}
	if !contains(td.Consumers, "WidgetReader") {
		t.Errorf("Widget.Consumers = %v, want to include WidgetReader", td.Consumers)
	}
	if len(rpt.BoundaryWarnings) != 0 {
		t.Errorf("unexpected boundary warnings: %v", rpt.BoundaryWarnings)
	}
}

func TestOwnership_RequestOnlyTypeIsUnowned(t *testing.T) {
	gw := gwtest.New()
	gw.Register(&WidgetReaderRouter{}) // only consumes Widget, nobody produces it
	rpt := introspect(t, gw)
	td, ok := rpt.Types["Widget"]
	if !ok {
		t.Fatalf("Widget type absent; types=%v", keysOf(rpt.Types))
	}
	if td.Owner != "" {
		t.Errorf("Widget.Owner = %q, want empty (no producer)", td.Owner)
	}
}

// Two producers returning the SAME shape is a shared domain model — info, not a
// warning. It is marked Shared, lists its co-owners, and does NOT drift.
func TestOwnership_TwoProducersSameShapeIsShared(t *testing.T) {
	gw := gwtest.New()
	gw.Register(&WidgetMakerRouter{})
	gw.Register(&SecondMakerRouter{})

	rpt := introspect(t, gw)
	td := rpt.Types["Widget"]
	if td.Owner != "" {
		t.Errorf("Widget.Owner = %q, want empty with multiple producers", td.Owner)
	}
	if !td.Shared {
		t.Errorf("Widget.Shared = false, want true (identical shape from 2 producers)")
	}
	if len(td.Owners) != 2 || !contains(td.Owners, "WidgetMaker") || !contains(td.Owners, "SecondMaker") {
		t.Errorf("Widget.Owners = %v, want [SecondMaker WidgetMaker]", td.Owners)
	}
	if contains(td.Consumers, "WidgetMaker") || contains(td.Consumers, "SecondMaker") {
		t.Errorf("Widget.Consumers = %v, must exclude producers", td.Consumers)
	}
	// Same shape → NOT drift, and NO boundary warning (that noise is gone).
	if _, drifted := rpt.CrossRefs["Widget"]; drifted {
		t.Errorf("Widget must not drift when both producers share a shape")
	}
	if len(rpt.BoundaryWarnings) != 0 {
		t.Errorf("shared same-shape type must not raise a boundary warning: %v", rpt.BoundaryWarnings)
	}
}

func keysOf(m map[string]TypeDescriptor) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
