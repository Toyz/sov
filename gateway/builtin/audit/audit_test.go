package audit

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/rpc"
)

type pingRouter struct{}

func (p *pingRouter) Ping(_ *rpc.Context) (map[string]bool, error) {
	return map[string]bool{"ok": true}, nil
}

func TestAudit_RecordsDispatchAndExposesRecent(t *testing.T) {
	var buf bytes.Buffer
	plugin := New(Config{Out: &buf})
	gw := gateway.New()
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
