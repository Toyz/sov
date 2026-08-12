package gateway_test

import (
	"context"
	"strings"
	"testing"

	. "github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/gwtest"
	"github.com/Toyz/sov/rpc"
)

// Unique named types referenced ONLY by hidden methods — if the type
// catalog leaks them, a hidden method's shape is still disclosed.
type SecretShape struct {
	SecretField string `json:"secret_field"`
}
type DebugShape struct {
	DebugField string `json:"debug_field"`
}

// HideProbeRouter declares one soft-hidden (secret) and one hard-hidden
// (debug) method, each carrying a unique named type, so both the method
// names AND their type surface can be checked in the introspect payloads.
type HideProbeRouter struct{}

func (r *HideProbeRouter) Open(ctx *rpc.Context) (string, error)                   { return "o", nil }
func (r *HideProbeRouter) Secret(ctx *rpc.Context, p *SecretShape) (string, error) { return "s", nil }
func (r *HideProbeRouter) Debug(ctx *rpc.Context, p *DebugShape) (string, error)   { return "d", nil }
func (r *HideProbeRouter) HiddenMethods() []string                                 { return []string{"secret"} }
func (r *HideProbeRouter) HardHiddenMethods() []string                             { return []string{"debug"} }

func TestIntrospect_HonorsHiddenMethods(t *testing.T) {
	gw := gwtest.New()
	gw.Register(&HideProbeRouter{})
	gw.ExposeIntrospect()

	pub := string(gw.IntrospectBody(context.Background(), &Request{Header: Header{}}).Body)
	full := string(gw.IntrospectBody(context.Background(), &Request{
		Header: Header{IntrospectInternalHeader: "1"},
	}).Body)

	// open: visible in both.
	if !strings.Contains(pub, `"open"`) {
		t.Errorf("public payload missing normal method 'open': %s", pub)
	}
	// secret (soft): omitted from public, present in full.
	if strings.Contains(pub, `"secret"`) {
		t.Errorf("SOFT-hidden 'secret' leaked into PUBLIC introspect payload")
	}
	if !strings.Contains(full, `"secret"`) {
		t.Errorf("SOFT-hidden 'secret' missing from FULL (internal-header) payload")
	}
	// debug (hard): absent from BOTH.
	if strings.Contains(pub, `"debug"`) {
		t.Errorf("HARD-hidden 'debug' leaked into PUBLIC introspect payload")
	}
	if strings.Contains(full, `"debug"`) {
		t.Errorf("HARD-hidden 'debug' leaked into FULL introspect payload")
	}

	// Type-surface leak: a hidden method's named types must not survive in
	// the public payload's type catalog (soft) / any payload (hard).
	if strings.Contains(pub, "SecretShape") || strings.Contains(pub, "secret_field") {
		t.Errorf("SOFT-hidden method's type (SecretShape) leaked into PUBLIC payload")
	}
	if strings.Contains(pub, "DebugShape") || strings.Contains(pub, "debug_field") {
		t.Errorf("HARD-hidden method's type (DebugShape) leaked into PUBLIC payload")
	}
	if strings.Contains(full, "DebugShape") || strings.Contains(full, "debug_field") {
		t.Errorf("HARD-hidden method's type (DebugShape) leaked into FULL payload")
	}
}

// GoCasedHideRouter makes the natural consumer mistake: returns the GO
// method name ("Secret") from HiddenMethods instead of the wire name
// ("secret"). The engine must normalize the casing so the method is still
// hidden — a silent no-op here would leak a method the author believes is
// hidden.
type GoCasedHideRouter struct{}

func (r *GoCasedHideRouter) Open(ctx *rpc.Context) (string, error)   { return "o", nil }
func (r *GoCasedHideRouter) Secret(ctx *rpc.Context) (string, error) { return "s", nil }
func (r *GoCasedHideRouter) HiddenMethods() []string                 { return []string{"Secret"} } // Go-cased

// The declared perm (HELL-280) must survive the introspect strip/split
// pipeline into the public catalog so the explorer/codegen can show it.
func TestIntrospect_EmitsPerm(t *testing.T) {
	gw := gwtest.New()
	gw.Register(&PermGuardedRouter{}) // AuthzRequirements: do -> pages:write
	gw.ExposeIntrospect()
	body := string(gw.IntrospectBody(context.Background(), &Request{Header: Header{}}).Body)
	if !strings.Contains(body, `"perm":"pages:write"`) {
		t.Fatalf("introspect body missing declared perm: %s", body)
	}
}

func TestIntrospect_GoCasedHiddenMarkerStillHides(t *testing.T) {
	gw := gwtest.New()
	gw.Register(&GoCasedHideRouter{})
	gw.ExposeIntrospect()
	pub := string(gw.IntrospectBody(context.Background(), &Request{Header: Header{}}).Body)
	if strings.Contains(pub, `"secret"`) {
		t.Errorf("Go-cased HiddenMethods([\"Secret\"]) failed to hide wire method 'secret': %s", pub)
	}
}
