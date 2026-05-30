package rpctest_test

import (
	"strings"
	"testing"

	"github.com/Toyz/sov/rpc"
	"github.com/Toyz/sov/rpctest"
)

type EchoRouter struct{}
type SayParams struct {
	Msg string `json:"msg"`
}

func (r *EchoRouter) Say(ctx *rpc.Context, p *SayParams) (map[string]string, error) {
	return map[string]string{"echoed": p.Msg}, nil
}

func (r *EchoRouter) WhoAmI(ctx *rpc.Context) (string, error) {
	u, err := rpc.UserFromContext(ctx)
	if err != nil {
		return "", err
	}
	return u.(string), nil
}

func (r *EchoRouter) MyScopes(ctx *rpc.Context) ([]string, error) {
	c := ctx.Claims()
	if c == nil {
		return nil, rpc.Unauthorized("no claims")
	}
	return c.Scopes, nil
}

func newEng(t *testing.T) *rpc.Engine {
	t.Helper()
	eng := rpc.NewEngine()
	eng.Register(&EchoRouter{})
	return eng
}

func TestRpcTest_CallInto(t *testing.T) {
	eng := newEng(t)
	var out map[string]string
	status, err := rpctest.CallInto(eng, rpctest.NewCtx(), "Echo", "say", &SayParams{Msg: "hi"}, &out)
	if err != nil || status != 200 {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if out["echoed"] != "hi" {
		t.Fatalf("out = %#v", out)
	}
}

func TestRpcTest_WithUser(t *testing.T) {
	eng := newEng(t)
	ctx := rpctest.New().WithUser("alice").Ctx()
	var name string
	status, err := rpctest.CallInto(eng, ctx, "Echo", "whoAmI", nil, &name)
	if err != nil || status != 200 || name != "alice" {
		t.Fatalf("status=%d err=%v name=%q", status, err, name)
	}
}

func TestRpcTest_WithScopesSetsClaims(t *testing.T) {
	eng := newEng(t)
	ctx := rpctest.New().WithScopes("bob", "chirp:write", "feed:read").Ctx()
	var scopes []string
	status, err := rpctest.CallInto(eng, ctx, "Echo", "myScopes", nil, &scopes)
	if err != nil || status != 200 {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if len(scopes) != 2 || scopes[0] != "chirp:write" || scopes[1] != "feed:read" {
		t.Fatalf("scopes = %#v", scopes)
	}
}

func TestRpcTest_ErrorPath(t *testing.T) {
	eng := newEng(t)
	status, _, err := rpctest.Call(eng, rpctest.NewCtx(), "Echo", "whoAmI", nil)
	if status != 401 {
		t.Fatalf("status = %d", status)
	}
	if err == nil {
		t.Fatal("expected error")
	}
	rerr, ok := err.(*rpc.Error)
	if !ok || rerr.Code != "UNAUTHORIZED" {
		t.Fatalf("err = %#v", err)
	}
	if !strings.Contains(rerr.Message, "authentication") {
		t.Fatalf("msg = %q", rerr.Message)
	}
}
