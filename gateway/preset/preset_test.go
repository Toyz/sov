package preset

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/audit"
	"github.com/Toyz/sov/gateway/builtin/hmacseal"
	"github.com/Toyz/sov/gateway/builtin/meshsecret"
	"github.com/Toyz/sov/gateway/builtin/registertoken"
	rtproto "github.com/Toyz/sov/gateway/builtin/registertoken/proto"
)

// pluginNames extracts PluginName() from every entry that exposes it.
func pluginNames(plugins []any) map[string]bool {
	names := map[string]bool{}
	for _, p := range plugins {
		if n, ok := p.(interface{ PluginName() string }); ok {
			names[n.PluginName()] = true
		}
	}
	return names
}

// Empty config = no join/seal gates wired. This is the documented (and
// previously un-closable) open-_register default for Monolith/Hybrid.
func TestMonolith_NoGatesByDefault(t *testing.T) {
	names := pluginNames(Monolith(MonolithConfig{}))
	for _, gate := range []string{"mesh-secret", "register-token", "hmac-seal"} {
		if names[gate] {
			t.Errorf("empty MonolithConfig wired %q — gates must be opt-in", gate)
		}
	}
	// Core plugins still present.
	if !names["registry"] {
		t.Error("registry plugin missing from Monolith set")
	}
}

// Audit is OPT-IN: no Out → no audit hook (no per-request identity/path
// logging by default); set Out → wired. Same as the Registry preset.
func TestMonolith_AuditOptIn(t *testing.T) {
	if pluginNames(Monolith(MonolithConfig{}))["audit"] {
		t.Error("audit wired with no Audit.Out — it must be opt-in (records every dispatch + subject)")
	}
	cfg := MonolithConfig{Audit: audit.Config{Out: io.Discard}}
	if !pluginNames(Monolith(cfg))["audit"] {
		t.Error("audit not wired when Audit.Out is set")
	}
}

// Each gate wires only when its secret/token is set — the fix that lets a
// Hybrid gateway close its _register endpoint through config.
func TestMonolith_GatesWireWhenConfigured(t *testing.T) {
	cfg := MonolithConfig{
		HMACSeal:      hmacseal.Config{Secret: []byte("s")},
		MeshSecret:    meshsecret.Config{Secret: []byte("m")},
		RegisterToken: registertoken.Config{Token: []byte("t")},
	}
	names := pluginNames(Monolith(cfg))
	for _, gate := range []string{"mesh-secret", "register-token", "hmac-seal"} {
		if !names[gate] {
			t.Errorf("configured gate %q not wired into Monolith set", gate)
		}
	}
}

// Hybrid is wired identically to Monolith, so the gates reach it too.
func TestHybrid_GatesWireWhenConfigured(t *testing.T) {
	names := pluginNames(Hybrid(HybridConfig{RegisterToken: registertoken.Config{Token: []byte("t")}}))
	if !names["register-token"] {
		t.Error("RegisterToken not wired into Hybrid set — _register stays open")
	}
}

// End-to-end security proof: the hole exists open by default, the
// RegisterToken gate actually CLOSES /rpc/_register through NewHybrid
// config, and a valid token still joins. Verifies the fix behaviorally,
// not just that the plugin lands in the set.
func TestHybrid_RegisterTokenClosesTheHoleEndToEnd(t *testing.T) {
	register := func(gw *gateway.Gateway, token string) int {
		h := gateway.Header{}
		if token != "" {
			h[rtproto.RegisterTokenHeader] = token
		}
		resp := gw.Handle(context.Background(), &gateway.Request{
			Method: http.MethodPost,
			Path:   "/rpc/_register",
			Header: h,
			Body:   []byte(`{"name":"Chirp","address":"http://localhost:9002","heartbeat_interval_seconds":5}`),
		})
		return resp.Status
	}

	// 1. OPEN by default — no gate configured, anyone registers. This is
	//    the documented default-open risk; assert it so a future "secure
	//    by default" flip is a deliberate, test-breaking decision.
	if got := register(NewHybrid(HybridConfig{}), ""); got != 200 {
		t.Errorf("ungated hybrid _register status=%d, want 200 (open by default)", got)
	}

	// 2. Gated, no/blank token → rejected.
	gated := func() *gateway.Gateway {
		return NewHybrid(HybridConfig{RegisterToken: registertoken.Config{Token: []byte("join-secret")}})
	}
	if got := register(gated(), ""); got == 200 {
		t.Error("gated hybrid accepted _register with NO token — gate not enforced")
	}
	if got := register(gated(), "wrong-token"); got == 200 {
		t.Error("gated hybrid accepted _register with WRONG token — gate not enforced")
	}

	// 3. Gated, correct token → joins.
	if got := register(gated(), "join-secret"); got != 200 {
		t.Errorf("gated hybrid rejected _register with CORRECT token: status=%d", got)
	}
}
