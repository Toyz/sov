package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// ---- Test routers ----------------------------------------------------------

type EchoRouter struct {
	Prefix string // kept ASCII-safe to keep test assertions json-stable
}

type EchoSayParams struct {
	Msg string `json:"msg"`
}

func (r *EchoRouter) Say(ctx *Context, p *EchoSayParams) (map[string]string, error) {
	return map[string]string{"echoed": r.Prefix + p.Msg}, nil
}

func (r *EchoRouter) Ping(ctx *Context) (map[string]bool, error) {
	return map[string]bool{"ok": true}, nil
}

func (r *EchoRouter) Crash(ctx *Context) error {
	return errors.New("kaboom")
}

func (r *EchoRouter) Refuse(ctx *Context) error {
	return Forbidden("nope")
}

func (r *EchoRouter) NeedsUser(ctx *Context) (any, error) {
	return UserFromContext(ctx)
}

// ---- Engine tests ----------------------------------------------------------

func newEcho(t *testing.T) *Engine {
	t.Helper()
	e := NewEngine()
	e.Register(&EchoRouter{Prefix: "pre:"})
	return e
}

func TestEngine_RegisterStripsRouterSuffix(t *testing.T) {
	e := newEcho(t)
	if got := e.Routers(); len(got) != 1 || got[0] != "Echo" {
		t.Fatalf("routers = %v", got)
	}
	methods := e.Methods("Echo")
	want := []string{"crash", "needsUser", "ping", "refuse", "say"}
	if strings.Join(methods, ",") != strings.Join(want, ",") {
		t.Fatalf("methods = %v, want %v", methods, want)
	}
}

func TestEngine_RegisterPanicsOnMissingSuffix(t *testing.T) {
	type NoSuffix struct{}
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		}
	}()
	NewEngine().Register(&NoSuffix{})
}

func TestEngine_RegisterPanicsOnDup(t *testing.T) {
	e := NewEngine()
	e.Register(&EchoRouter{})
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		}
	}()
	e.Register(&EchoRouter{})
}

func TestDispatch_WithParams(t *testing.T) {
	status, body := newEcho(t).Dispatch(NewContext(context.Background()), "Echo", "say", []byte(`{"args":[{"msg":"hi"}]}`))
	if status != 200 {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	if !strings.Contains(string(body), `"echoed":"pre:hi"`) {
		t.Fatalf("body = %s", body)
	}
}

func TestDispatch_NoParams(t *testing.T) {
	status, body := newEcho(t).Dispatch(NewContext(context.Background()), "Echo", "ping", nil)
	if status != 200 {
		t.Fatalf("status = %d", status)
	}
	if !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("body = %s", body)
	}
}

func TestDispatch_UnknownRouter(t *testing.T) {
	status, body := newEcho(t).Dispatch(NewContext(context.Background()), "Nope", "x", nil)
	if status != 404 {
		t.Fatalf("status = %d", status)
	}
	if !strings.Contains(string(body), `"NOT_FOUND"`) || !strings.Contains(string(body), "Nope") {
		t.Fatalf("body = %s", body)
	}
}

func TestDispatch_UnknownMethod(t *testing.T) {
	status, body := newEcho(t).Dispatch(NewContext(context.Background()), "Echo", "shout", nil)
	if status != 404 {
		t.Fatalf("status = %d", status)
	}
	if !strings.Contains(string(body), "shout") {
		t.Fatalf("body = %s", body)
	}
}

func TestDispatch_TypedErrorPassesThrough(t *testing.T) {
	status, body := newEcho(t).Dispatch(NewContext(context.Background()), "Echo", "refuse", nil)
	if status != 403 {
		t.Fatalf("status = %d", status)
	}
	if !strings.Contains(string(body), `"FORBIDDEN"`) || !strings.Contains(string(body), "nope") {
		t.Fatalf("body = %s", body)
	}
}

func TestDispatch_UnknownErrorHidesDetail(t *testing.T) {
	status, body := newEcho(t).Dispatch(NewContext(context.Background()), "Echo", "crash", nil)
	if status != 500 {
		t.Fatalf("status = %d", status)
	}
	if strings.Contains(string(body), "kaboom") {
		t.Fatalf("body leaked detail: %s", body)
	}
}

func TestDispatch_UserFromContext(t *testing.T) {
	ctx := NewContext(context.Background())
	status, _ := newEcho(t).Dispatch(ctx, "Echo", "needsUser", nil)
	if status != 401 {
		t.Fatalf("status = %d (expected 401 for anonymous)", status)
	}

	ctx.User = "alice"
	status, body := newEcho(t).Dispatch(ctx, "Echo", "needsUser", nil)
	if status != 200 {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	var resp SuccessResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Data != "alice" {
		t.Fatalf("data = %v", resp.Data)
	}
}

func TestDispatch_BadJSONBody(t *testing.T) {
	status, body := newEcho(t).Dispatch(NewContext(context.Background()), "Echo", "say", []byte(`not json`))
	if status != 400 {
		t.Fatalf("status = %d", status)
	}
	if !strings.Contains(string(body), `"BAD_REQUEST"`) {
		t.Fatalf("body = %s", body)
	}
}

func TestDispatch_BadParamsShape(t *testing.T) {
	status, body := newEcho(t).Dispatch(NewContext(context.Background()), "Echo", "say", []byte(`{"args":[{"msg":42}]}`))
	if status != 400 {
		t.Fatalf("status = %d, body = %s", status, body)
	}
}

func FuzzDispatch(f *testing.F) {
	e := NewEngine()
	e.Register(&EchoRouter{Prefix: "pre:"})
	f.Add("Echo", "say", []byte(`{"args":[{"msg":"x"}]}`))
	f.Add("Echo", "ping", []byte(``))
	f.Add("Nope", "y", []byte(``))
	f.Add("Echo", "say", []byte(`{"args":`))
	f.Fuzz(func(t *testing.T, router, method string, body []byte) {
		_, _ = e.Dispatch(NewContext(context.Background()), router, method, body)
	})
}
