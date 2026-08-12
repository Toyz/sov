package rpc

import (
	"fmt"
	"strings"
	"testing"
)

// ---- Test routers ----------------------------------------------------------

// PermTagParams carries a method-level perm via the blank `_` sentinel.
type PermTagParams struct {
	_    struct{} `sov:"perm=pages:write"`
	Body string   `json:"body"`
}

// PermRouter mixes tag-declared and marker-declared perms so precedence
// and gap-filling can both be asserted.
type PermRouter struct{}

func (r *PermRouter) Write(ctx *Context, p *PermTagParams) error { return nil } // perm via tag
func (r *PermRouter) Read(ctx *Context) error                    { return nil } // perm via marker only
func (r *PermRouter) Open(ctx *Context) error                    { return nil } // no perm anywhere

// AuthzRequirements: Read gets a perm from the marker; Write is also named
// here with a DIFFERENT value to prove the tag wins; Open is left out.
func (r *PermRouter) AuthzRequirements() map[string]string {
	return map[string]string{
		"read":  "pages:read",
		"write": "marker:loses", // tag on Write must win over this
	}
}

func TestPerm_TagWinsMarkerFillsGaps(t *testing.T) {
	e := NewEngine()
	e.Register(&PermRouter{})

	if got := e.Perm("Perm", "write"); got != "pages:write" {
		t.Fatalf("write perm = %q, want tag value %q (tag must win over marker)", got, "pages:write")
	}
	if got := e.Perm("Perm", "read"); got != "pages:read" {
		t.Fatalf("read perm = %q, want marker value %q", got, "pages:read")
	}
	if got := e.Perm("Perm", "open"); got != "" {
		t.Fatalf("open perm = %q, want empty (undeclared)", got)
	}
}

func TestPerm_UnknownMethodInMarkerPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic naming unknown method in AuthzRequirements")
		}
	}()
	NewEngine().Register(&badMarkerRouter{})
}

type badMarkerRouter struct{}

func (r *badMarkerRouter) Do(ctx *Context) error { return nil }
func (r *badMarkerRouter) AuthzRequirements() map[string]string {
	return map[string]string{"nope": "x"} // "nope" is not a method
}

func TestPerm_SurfacedInDescribe(t *testing.T) {
	e := NewEngine()
	e.Register(&PermRouter{})
	for _, rd := range e.Describe() {
		if rd.Router != "Perm" {
			continue
		}
		for _, md := range rd.Methods {
			if md.Method == "write" && md.Perm != "pages:write" {
				t.Fatalf("describe write perm = %q, want %q", md.Perm, "pages:write")
			}
			if md.Method == "open" && md.Perm != "" {
				t.Fatalf("describe open perm = %q, want empty", md.Perm)
			}
		}
	}
}

// ---- HELL-283: router-local MethodAuthorizer hook -------------------------

type GuardParams struct {
	WorkspaceID string `json:"workspace_id"`
}

type GuardRouter struct{}

func (r *GuardRouter) Write(ctx *Context, p *GuardParams) (string, error) {
	return "wrote:" + p.WorkspaceID, nil
}
func (r *GuardRouter) Ping(ctx *Context) (string, error) { return "pong", nil }

// AuthorizeMethod denies writes to a specific workspace, in-process, from
// the decoded params — the resource-scoped tier.
func (r *GuardRouter) AuthorizeMethod(ctx *Context, method string, params any) error {
	if method == "write" {
		p := params.(*GuardParams)
		if p.WorkspaceID == "ws_denied" {
			return Forbidden("no write on %s", p.WorkspaceID)
		}
	}
	return nil // no-param methods (ping) arrive with params == nil
}

func TestMethodAuthorizer_AllowDenySkipsHandler(t *testing.T) {
	e := NewEngine()
	e.Register(&GuardRouter{})

	st, body := e.Dispatch(&Context{}, "Guard", "write", []byte(`{"args":{"workspace_id":"ws_ok"}}`))
	if st != 200 || !strings.Contains(string(body), "wrote:ws_ok") {
		t.Fatalf("allow: st=%d body=%s", st, body)
	}

	st, body = e.Dispatch(&Context{}, "Guard", "write", []byte(`{"args":{"workspace_id":"ws_denied"}}`))
	if st != 403 {
		t.Fatalf("deny status = %d body=%s, want 403", st, body)
	}
	if strings.Contains(string(body), "wrote:") {
		t.Fatalf("handler ran despite deny: %s", body)
	}
}

func TestMethodAuthorizer_NilParamsForNoParamMethod(t *testing.T) {
	e := NewEngine()
	e.Register(&GuardRouter{})
	// ping takes no params — the hook must receive nil without panicking.
	st, body := e.Dispatch(&Context{}, "Guard", "ping", nil)
	if st != 200 || !strings.Contains(string(body), "pong") {
		t.Fatalf("ping: st=%d body=%s", st, body)
	}
}

// ---- HELL-282: casing-aware not-found hint --------------------------------

func TestDispatch_CasingHintOnNotFound(t *testing.T) {
	e := newEcho(t)
	// Echo has wire method "say"; a caller sending Go casing "Say" must get
	// a hint, not a bare not-found.
	status, body := e.Dispatch(&Context{}, "Echo", "Say", nil)
	if status != 404 {
		t.Fatalf("status = %d, want 404", status)
	}
	s := string(body)
	// Body is JSON, so the suggested name's quotes are backslash-escaped;
	// assert on the hint phrase and the wire name separately.
	if !strings.Contains(s, "did you mean") || !strings.Contains(s, "say") {
		t.Fatalf("body = %s, want casing hint for \"say\"", s)
	}
}

func TestDispatch_NoHintWhenGenuinelyMissing(t *testing.T) {
	e := newEcho(t)
	status, body := e.Dispatch(&Context{}, "Echo", "teleport", nil)
	if status != 404 {
		t.Fatalf("status = %d, want 404", status)
	}
	if strings.Contains(string(body), "did you mean") {
		t.Fatalf("body = %s, should not hint for a truly missing method", body)
	}
}

// ---- HELL-281: stray reserved-marker warning ------------------------------

// StrayRouter names an RPC-shaped method "Apply" — a reserved marker. It
// compiles and looks normal but is silently dropped from the wire; the
// engine must warn.
type StrayRouter struct{}

func (r *StrayRouter) Apply(ctx *Context) error { return nil } // reserved marker collision
func (r *StrayRouter) Live(ctx *Context) error  { return nil } // a real exposed method

// captureLogger records Warn calls so the stray-marker test can assert the
// engine warned through its injected structured logger (not stdlib log).
type captureLogger struct{ warns []string }

func (c *captureLogger) Warn(msg string, args ...any) {
	c.warns = append(c.warns, msg+" "+fmt.Sprint(args...))
}

func TestRegister_WarnsOnStrayReservedMarker(t *testing.T) {
	cap := &captureLogger{}
	e := NewEngine()
	e.SetLogger(cap)
	e.Register(&StrayRouter{})

	var hit bool
	for _, m := range cap.warns {
		if strings.Contains(m, "Apply") && strings.Contains(m, "reserved") {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("expected a stray-marker warning naming Apply, got %v", cap.warns)
	}
}
