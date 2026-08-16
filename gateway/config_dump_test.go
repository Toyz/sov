package gateway_test

import (
	"encoding/json"
	"net/http"
	"testing"

	. "github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/internal/gwtest"
)

// _config is opt-in: a bare gateway must 404 it (method-agnostically) so it
// looks ABSENT rather than leaking its existence.
func TestConfig_ClosedByDefault404(t *testing.T) {
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		resp := do(t, newGateway(t), m, "/rpc/_config", nil)
		if resp.Status != 404 {
			t.Fatalf("%s closed _config status = %d, body = %s", m, resp.Status, resp.Body)
		}
	}
}

func TestConfig_ExposedReturnsSanitizedReport(t *testing.T) {
	gw := newGateway(t)
	gw.ExposeConfig()
	resp := do(t, gw, http.MethodGet, "/rpc/_config", nil)
	if resp.Status != 200 {
		t.Fatalf("status = %d, body = %s", resp.Status, resp.Body)
	}
	var rep ConfigReport
	if err := json.Unmarshal(resp.Body, &rep); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, resp.Body)
	}
	// The locally-served router shows up.
	found := false
	for _, r := range rep.Routers {
		if r == "Echo" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Echo not in routers: %v", rep.Routers)
	}
	// Effective breaker config is reported (defaults: threshold 5, cooldown 10s).
	if rep.RemoteBreaker.FailureThreshold == 0 || rep.RemoteBreaker.Cooldown == "" {
		t.Fatalf("breaker not reported: %+v", rep.RemoteBreaker)
	}
	// Wired plugins are enumerated (name only, never config).
	if rep.Plugins == nil {
		t.Fatalf("plugins nil")
	}
}

// A tuned knob is reflected in the dump — proving it reads live gateway state,
// not defaults.
func TestConfig_ReflectsTunedKnobs(t *testing.T) {
	gw := gwtest.New(WithMaxInFlight(7))
	gw.ExposeConfig()
	resp := do(t, gw, http.MethodGet, "/rpc/_config", nil)
	if resp.Status != 200 {
		t.Fatalf("status = %d, body = %s", resp.Status, resp.Body)
	}
	var rep ConfigReport
	if err := json.Unmarshal(resp.Body, &rep); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rep.MaxInFlight != 7 {
		t.Fatalf("max_in_flight = %d, want 7", rep.MaxInFlight)
	}
}
