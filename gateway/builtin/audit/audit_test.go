package audit

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/internal/gwtest"
	"github.com/Toyz/sov/rpc"
)

type pingRouter struct{}

func (p *pingRouter) Ping(_ *rpc.Context) (map[string]bool, error) {
	return map[string]bool{"ok": true}, nil
}

func TestAudit_RecordsDispatchAndExposesRecent(t *testing.T) {
	var buf bytes.Buffer
	plugin := New(Config{Out: &buf})
	gw := gwtest.New()
	gw.Register(&pingRouter{})
	if err := gw.Use(plugin); err != nil {
		t.Fatalf("Use: %v", err)
	}
	// Fire two dispatches.
	for i := 0; i < 2; i++ {
		resp := gw.Handle(context.Background(), &gateway.Request{
			Method: http.MethodPost, Path: "/rpc/ping/ping",
			Header: gateway.Header{}, Body: []byte(`{"args":{}}`),
		})
		if resp == nil {
			t.Fatal("nil response")
		}
	}
	if !strings.Contains(buf.String(), `"router":"ping"`) {
		t.Fatalf("audit log missing router field: %s", buf.String())
	}
	// Call Audit.recent through the gateway — proves the plugin is
	// also registered as a router.
	resp := gw.Handle(context.Background(), &gateway.Request{
		Method: http.MethodPost, Path: "/rpc/Audit/recent",
		Header: gateway.Header{}, Body: []byte(`{"args":{"limit":10}}`),
	})
	if resp.Status != 200 {
		t.Fatalf("Audit.recent status=%d body=%s", resp.Status, resp.Body)
	}
	if !strings.Contains(string(resp.Body), `"events"`) {
		t.Fatalf("body missing events: %s", resp.Body)
	}
}

// recentEvents must return events while the ring is still FILLING (growth
// phase), not just after it wraps — a regression the "contains events" check
// above never caught. Also verifies newest-first order across the wrap.
func TestAudit_RecentDuringGrowthAndWrap(t *testing.T) {
	a := New(Config{RingSize: 3})
	methods := func() []string {
		var ms []string
		for _, ev := range a.recentEvents(10) {
			ms = append(ms, ev.Method)
		}
		return ms
	}

	a.OnDispatch(gateway.DispatchEvent{Method: "m1"})
	a.OnDispatch(gateway.DispatchEvent{Method: "m2"})
	if got := methods(); len(got) != 2 || got[0] != "m2" || got[1] != "m1" {
		t.Fatalf("growth (ring not full): want [m2 m1], got %v", got)
	}

	a.OnDispatch(gateway.DispatchEvent{Method: "m3"})
	a.OnDispatch(gateway.DispatchEvent{Method: "m4"}) // evicts m1
	a.OnDispatch(gateway.DispatchEvent{Method: "m5"}) // evicts m2
	if got := methods(); len(got) != 3 || got[0] != "m5" || got[2] != "m3" {
		t.Fatalf("wrap: want [m5 m4 m3], got %v", got)
	}

	if got := a.recentEvents(1); len(got) != 1 || got[0].Method != "m5" {
		t.Fatalf("limit 1: want newest [m5], got %v", got)
	}
}
