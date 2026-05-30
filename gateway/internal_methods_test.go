package gateway_test

import (
	. "github.com/Toyz/sov/gateway"

	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Toyz/sov/rpc"
)

// ---- Test routers ----------------------------------------------------------

// DemoAuthzRouter implements AuthzService → binds DemoAuthz/check, which
// the gateway hard-hides automatically.
type DemoAuthzRouter struct{}

func (r *DemoAuthzRouter) Check(ctx *rpc.Context, p *CheckParams) (*AuthzDecision, error) {
	return &AuthzDecision{Allow: true}, nil
}

// SoftHideRouter declares one soft-hidden method via the marker. secret
// returns a unique named type so we can assert type-pruning.
type SoftHideRouter struct{}

type SecretBlob struct {
	Token string `json:"token"`
}

func (r *SoftHideRouter) Open(ctx *rpc.Context) (map[string]bool, error) {
	return map[string]bool{"ok": true}, nil
}
func (r *SoftHideRouter) Secret(ctx *rpc.Context) (*SecretBlob, error) {
	return &SecretBlob{Token: "x"}, nil
}
func (r *SoftHideRouter) HiddenMethods() []string { return []string{"secret"} }

// TagHideRouter uses the sov sentinel for soft (peek) and hard (vault).
type TagHideRouter struct{}

type PeekParams struct {
	_  struct{} `sov:"internal"`
	ID string   `json:"id"`
}
type VaultParams struct {
	_  struct{} `sov:"internal,hard"`
	ID string   `json:"id"`
}

func (r *TagHideRouter) Plain(ctx *rpc.Context) (map[string]bool, error) {
	return map[string]bool{"ok": true}, nil
}
func (r *TagHideRouter) Peek(ctx *rpc.Context, p *PeekParams) (map[string]string, error) {
	return map[string]string{"id": p.ID}, nil
}
func (r *TagHideRouter) Vault(ctx *rpc.Context, p *VaultParams) (map[string]string, error) {
	return map[string]string{"id": p.ID}, nil
}

// OpsRouter hard-hides debugDump via the marker — invisible yet callable.
type OpsRouter struct{}

func (r *OpsRouter) DebugDump(ctx *rpc.Context) (map[string]string, error) {
	return map[string]string{"note": "diag"}, nil
}
func (r *OpsRouter) HardHiddenMethods() []string { return []string{"debugDump"} }

// ---- Helpers ---------------------------------------------------------------

func introspectReport(t *testing.T, gw *Gateway, internal bool) *IntrospectReport {
	t.Helper()
	gw.ExposeIntrospect() // endpoint is opt-in; these tests assert its body
	h := Header{}
	if internal {
		h[IntrospectInternalHeader] = "1"
	}
	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodGet, Path: "/rpc/_introspect", Header: h,
	})
	if resp.Status != 200 {
		t.Fatalf("introspect status = %d, body = %s", resp.Status, resp.Body)
	}
	var rpt IntrospectReport
	if err := json.Unmarshal(resp.Body, &rpt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return &rpt
}

// methodNames returns the set of "Router.method" present for a router.
func methodNames(rpt *IntrospectReport, router string) map[string]bool {
	out := map[string]bool{}
	for _, rds := range rpt.Services {
		for _, rd := range rds {
			if rd.Router != router {
				continue
			}
			for _, md := range rd.Methods {
				out[md.Method] = true
			}
		}
	}
	return out
}

func hasService(rpt *IntrospectReport, router string) bool {
	for _, rds := range rpt.Services {
		for _, rd := range rds {
			if rd.Router == router {
				return true
			}
		}
	}
	return false
}

// ---- Tests -----------------------------------------------------------------

func TestInternal_AuthzCheckHardHidden(t *testing.T) {
	gw := New()
	gw.RegisterAuth(&AuthRouter{})
	gw.RegisterAuthz(&DemoAuthzRouter{})

	// Default payload: verify + the whole DemoAuthz router (only check) gone.
	pub := introspectReport(t, gw, false)
	if methodNames(pub, "Auth")["verify"] {
		t.Error("Auth.verify present in default payload (should be hard-hidden)")
	}
	if hasService(pub, "DemoAuthz") {
		t.Error("DemoAuthz service present in default payload (only method check, hard-hidden → router dropped)")
	}

	// Full payload (X-Sov-Introspect-Internal): STILL absent — hard-hide
	// is never revealed.
	full := introspectReport(t, gw, true)
	if methodNames(full, "Auth")["verify"] {
		t.Error("Auth.verify present in internal payload (hard-hide must never reveal)")
	}
	if hasService(full, "DemoAuthz") {
		t.Error("DemoAuthz present in internal payload (hard-hide must never reveal)")
	}
}

func TestInternal_SoftHideMarkerAndTag(t *testing.T) {
	gw := New()
	gw.Register(&SoftHideRouter{})
	gw.Register(&TagHideRouter{})

	pub := introspectReport(t, gw, false)
	sh := methodNames(pub, "SoftHide")
	if sh["secret"] {
		t.Error("SoftHide.secret present by default (soft-hidden via marker)")
	}
	if !sh["open"] {
		t.Error("SoftHide.open missing — non-hidden sibling should remain")
	}
	th := methodNames(pub, "TagHide")
	if th["peek"] {
		t.Error("TagHide.peek present by default (soft-hidden via sov tag)")
	}
	if !th["plain"] {
		t.Error("TagHide.plain missing — non-hidden sibling should remain")
	}

	full := introspectReport(t, gw, true)
	if !methodNames(full, "SoftHide")["secret"] {
		t.Error("SoftHide.secret missing from internal payload (should be revealed)")
	}
	if !methodNames(full, "TagHide")["peek"] {
		t.Error("TagHide.peek missing from internal payload (should be revealed)")
	}
	// And it carries the internal flag in the full payload.
	if !methodFlagged(full, "SoftHide", "secret") {
		t.Error("SoftHide.secret not flagged internal:true in internal payload")
	}
}

func TestInternal_HardTagAlsoNeverRevealed(t *testing.T) {
	gw := New()
	gw.Register(&TagHideRouter{})

	for _, internal := range []bool{false, true} {
		rpt := introspectReport(t, gw, internal)
		if methodNames(rpt, "TagHide")["vault"] {
			t.Errorf("TagHide.vault present (internal=%v) — sov:\"internal,hard\" must never appear", internal)
		}
	}
}

func TestInternal_TypePrunedWhenOnlyUsedBySoftMethod(t *testing.T) {
	gw := New()
	gw.Register(&SoftHideRouter{})

	pub := introspectReport(t, gw, false)
	if _, ok := pub.Types["SecretBlob"]; ok {
		t.Error("SecretBlob in default .types — type used only by soft-hidden method must be pruned")
	}
	full := introspectReport(t, gw, true)
	if _, ok := full.Types["SecretBlob"]; !ok {
		t.Error("SecretBlob missing from internal .types — should be present when soft method revealed")
	}
}

func TestInternal_HardHiddenStillDispatchable(t *testing.T) {
	gw := New()
	gw.Register(&OpsRouter{})

	// Absent from both payloads.
	for _, internal := range []bool{false, true} {
		rpt := introspectReport(t, gw, internal)
		if methodNames(rpt, "Ops")["debugDump"] {
			t.Errorf("Ops.debugDump present (internal=%v) — hard-hidden must never appear", internal)
		}
	}

	// Yet a direct call still dispatches — hard-hide is discoverability,
	// not access control.
	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/Ops/debugDump", Header: Header{}, Body: []byte(`{"args":[]}`),
	})
	if resp.Status != 200 {
		t.Fatalf("debugDump status = %d, body = %s (hard-hidden method must stay callable)", resp.Status, resp.Body)
	}
}

// methodFlagged reports whether router.method carries Internal:true.
func methodFlagged(rpt *IntrospectReport, router, method string) bool {
	for _, rds := range rpt.Services {
		for _, rd := range rds {
			if rd.Router != router {
				continue
			}
			for _, md := range rd.Methods {
				if md.Method == method {
					return md.Internal
				}
			}
		}
	}
	return false
}
