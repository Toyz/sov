package users

import (
	"testing"

	"github.com/Toyz/sov/rpc"
	"github.com/Toyz/sov/rpctest"
)

// These tests show the sov test story: a handler test is a function call.
// rpctest.CallInto dispatches into the engine — no HTTP, no gateway, no mesh.

func newEngine(t *testing.T) *rpc.Engine {
	t.Helper()
	eng := rpc.NewEngine()
	eng.Register(&UserRouter{Store: NewMemoryStore()})
	return eng
}

func TestUser_RegisterThenGet(t *testing.T) {
	eng := newEngine(t)
	ctx := rpctest.NewCtx()

	var created User
	if _, err := rpctest.CallInto(eng, ctx, "User", "register",
		RegisterParams{Subject: "u_alice", Handle: "alice", Display: "Alice"}, &created); err != nil {
		t.Fatalf("register: %v", err)
	}
	if created.ID != "u_alice" || created.Handle != "alice" || created.Display != "Alice" {
		t.Fatalf("created = %+v", created)
	}

	var got User
	if _, err := rpctest.CallInto(eng, ctx, "User", "get", GetParams{Handle: "alice"}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != "u_alice" {
		t.Fatalf("get by handle = %+v", got)
	}
}

func TestUser_DuplicateHandleConflicts(t *testing.T) {
	eng := newEngine(t)
	ctx := rpctest.NewCtx()

	if _, err := rpctest.CallInto(eng, ctx, "User", "register",
		RegisterParams{Subject: "u_alice", Handle: "alice"}, &User{}); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if _, err := rpctest.CallInto(eng, ctx, "User", "register",
		RegisterParams{Subject: "u_bob", Handle: "alice"}, &User{}); err == nil {
		t.Fatal("registering a duplicate handle must conflict")
	}
}

func TestUser_GetMeUsesInjectedSubject(t *testing.T) {
	eng := newEngine(t)
	if _, err := rpctest.CallInto(eng, rpctest.NewCtx(), "User", "register",
		RegisterParams{Subject: "u_alice", Handle: "alice"}, &User{}); err != nil {
		t.Fatalf("seed register: %v", err)
	}

	// GetMe reads the subject the gateway would have injected — rpctest sets it.
	ctx := rpctest.New().WithUser("u_alice").Ctx()
	var me User
	if _, err := rpctest.CallInto(eng, ctx, "User", "getMe", nil, &me); err != nil {
		t.Fatalf("getMe: %v", err)
	}
	if me.Handle != "alice" {
		t.Fatalf("getMe = %+v", me)
	}
}
