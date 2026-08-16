package mcp_test

import (
	"testing"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/mcp"
	"github.com/Toyz/sov/rpc"
)

// mcpAuth is a minimal AuthService: presence of any bearer verifies to a
// subject. Gives the gateway an AuthBinding (what the tools/list gate checks).
type mcpAuth struct{}

func (mcpAuth) Verify(_ *rpc.Context, _ *gateway.VerifyParams) (*gateway.Claims, error) {
	return &gateway.Claims{Subject: "u1"}, nil
}

func TestMCP_ToolsListGatedWhenRequired(t *testing.T) {
	gw := gateway.New()
	gw.Register(&NoteToolsRouter{})
	gw.RegisterAuth(&mcpAuth{})
	gw.MustUse(mcp.New(mcp.Config{RequireAuthForList: true}))

	// Anonymous: gated → JSON-RPC error, no tool catalog leaked.
	out := mcpPost(t, gw, "", "tools/list", map[string]any{})
	if _, ok := out["error"]; !ok {
		t.Fatalf("anon tools/list must be gated (error), got %v", out)
	}
	if _, ok := out["result"]; ok {
		t.Fatalf("gated tools/list must not return a result: %v", out)
	}

	// Authenticated: tools returned.
	authed := mcpPost(t, gw, "good", "tools/list", map[string]any{})
	if _, ok := authed["result"]; !ok {
		t.Fatalf("authed tools/list should return tools: %v", authed)
	}
}

func TestMCP_ToolsListOpenByDefault(t *testing.T) {
	gw := gateway.New()
	gw.Register(&NoteToolsRouter{})
	gw.RegisterAuth(&mcpAuth{})
	gw.MustUse(mcp.New()) // default: not gated

	out := mcpPost(t, gw, "", "tools/list", map[string]any{})
	if _, ok := out["result"]; !ok {
		t.Fatalf("default tools/list should be open to anon: %v", out)
	}
}
