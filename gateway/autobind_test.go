package gateway_test

import (
	. "github.com/Toyz/sov/gateway"

	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Toyz/sov/rpc"
)

// auto-bind test routers — each satisfies the role interface implicitly.

type AutoAuthRouter struct{}

func (r *AutoAuthRouter) Verify(_ *rpc.Context, p *VerifyParams) (*Claims, error) {
	return &Claims{Subject: "u_auto_" + p.Token}, nil
}

type AutoAuthzRouter struct{}

func (r *AutoAuthzRouter) Check(_ *rpc.Context, p *CheckParams) (*AuthzDecision, error) {
	return &AuthzDecision{Allow: true}, nil
}

type SecondAuthRouter struct{}

func (r *SecondAuthRouter) Verify(_ *rpc.Context, p *VerifyParams) (*Claims, error) {
	return &Claims{Subject: "second"}, nil
}

func TestRegister_AutoDetectsAuthService(t *testing.T) {
	gw := New()
	gw.Register(&AutoAuthRouter{})
	if gw.AuthBindingForTest() == nil || gw.AuthBindingForTest().Service != "AutoAuth" || gw.AuthBindingForTest().Method != "verify" {
		t.Fatalf("expected auto-bind to AutoAuth/verify, got %+v", gw.AuthBindingForTest())
	}

	// Exercise: bearer flows through the auto-bound verifier.
	gw.Register(&WhoRouter{})
	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/Who/me",
		Header: Header{"Authorization": "Bearer alice"},
	})
	if resp.Status != 200 || !strings.Contains(string(resp.Body), "u_auto_alice") {
		t.Fatalf("auto-bound verifier did not run: status=%d body=%s", resp.Status, resp.Body)
	}
}

func TestRegister_AutoDetectsAuthzService(t *testing.T) {
	gw := New()
	gw.Register(&AutoAuthzRouter{})
	if gw.AuthzBindingForTest() == nil || gw.AuthzBindingForTest().Service != "AutoAuthz" || gw.AuthzBindingForTest().Method != "check" {
		t.Fatalf("expected auto-bind to AutoAuthz/check, got %+v", gw.AuthzBindingForTest())
	}
}

func TestRegister_PanicsOnDualAuthImplementers(t *testing.T) {
	gw := New()
	gw.Register(&AutoAuthRouter{})

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on second AuthService implementer")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "two services satisfy AuthService") {
			t.Fatalf("wrong panic message: %v", r)
		}
	}()
	gw.Register(&SecondAuthRouter{})
}

func TestRegister_SameAuthRouterTwiceIsIdempotent(t *testing.T) {
	gw := New()
	// First call binds via RegisterAuth, second via plain Register —
	// same router, same wire name. Should NOT panic.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("idempotent re-register panicked: %v", r)
		}
	}()
	r := &AutoAuthRouter{}
	gw.RegisterAuth(r)
	// Engine refuses dup router registration; bypass by calling bindAuth
	// directly — proves the guard is "different service" not "called twice".
	gw.BindAuthRaw("AutoAuth", "verify")
}
