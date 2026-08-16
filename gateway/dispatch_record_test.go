package gateway_test

import (
	"context"
	"net/http"
	"sync"
	"testing"

	. "github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/internal/gwtest"
)

// eventRecorder is a DispatchHook plugin that captures every event.
type eventRecorder struct {
	mu     sync.Mutex
	events []DispatchEvent
}

func (r *eventRecorder) PluginName() string { return "test-event-recorder" }
func (r *eventRecorder) OnDispatch(ev DispatchEvent) error {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
	return nil
}
func (r *eventRecorder) forPath(p string) []DispatchEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []DispatchEvent
	for _, e := range r.events {
		if e.Path == p {
			out = append(out, e)
		}
	}
	return out
}

// An authz denial short-circuits before handle() runs. Before W0.1 it emitted
// no DispatchEvent, so audit + metrics were blind to 403s. It must now record.
func TestDispatchRecord_DenialEmitsEventWithRemoteIP(t *testing.T) {
	gw := gwtest.New()
	rec := &eventRecorder{}
	if err := gw.Use(rec); err != nil {
		t.Fatalf("Use recorder: %v", err)
	}
	gw.RegisterAuth(&AuthRouter{})
	gw.RegisterAuthz(&AuthzRouter{denyMethod: "deleteAll"})
	gw.Register(&WhoRouter{})

	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/Who/deleteAll",
		Header:   Header{"Authorization": "Bearer good-x"},
		RemoteIP: "203.0.113.7",
	})
	if resp.Status != 403 {
		t.Fatalf("want 403, got %d body=%s", resp.Status, resp.Body)
	}
	evs := rec.forPath("/rpc/Who/deleteAll")
	if len(evs) != 1 {
		t.Fatalf("authz denial must emit exactly one DispatchEvent, got %d", len(evs))
	}
	if evs[0].Status != 403 {
		t.Fatalf("event status = %d, want 403", evs[0].Status)
	}
	if evs[0].RemoteIP != "203.0.113.7" {
		t.Fatalf("event RemoteIP = %q, want 203.0.113.7", evs[0].RemoteIP)
	}
}

// A call that passes auth/authz is recorded by handle. The new outermost
// recording middleware must NOT record it a second time.
func TestDispatchRecord_AllowedRecordedExactlyOnce(t *testing.T) {
	gw := gwtest.New()
	rec := &eventRecorder{}
	if err := gw.Use(rec); err != nil {
		t.Fatalf("Use recorder: %v", err)
	}
	gw.RegisterAuth(&AuthRouter{})
	gw.RegisterAuthz(&AuthzRouter{}) // allow all
	gw.Register(&WhoRouter{})

	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/Who/me",
		Header:   Header{"Authorization": "Bearer good-x"},
		RemoteIP: "198.51.100.2",
	})
	if resp.Status != 200 {
		t.Fatalf("want 200, got %d body=%s", resp.Status, resp.Body)
	}
	evs := rec.forPath("/rpc/Who/me")
	if len(evs) != 1 {
		t.Fatalf("allowed call recorded %d times, want exactly 1 (no double-record)", len(evs))
	}
	if evs[0].RemoteIP != "198.51.100.2" {
		t.Fatalf("event RemoteIP = %q, want 198.51.100.2", evs[0].RemoteIP)
	}
}
