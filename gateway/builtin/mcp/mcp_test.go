package mcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/mcp"
	"github.com/Toyz/sov/rpc"
)

// ---- test routers ---------------------------------------------------------

type AuthRouter struct{}

func (AuthRouter) Verify(ctx *rpc.Context, p *gateway.VerifyParams) (*gateway.Claims, error) {
	if !strings.HasPrefix(p.Token, "good-") {
		return nil, rpc.Unauthorized("bad token")
	}
	return &gateway.Claims{
		Subject:   "u_" + strings.TrimPrefix(p.Token, "good-"),
		ExpiresAt: time.Now().Add(time.Hour).UTC(),
	}, nil
}

type WhoRouter struct{}

func (WhoRouter) Me(ctx *rpc.Context) (string, error) { return rpc.RequireSubject(ctx) }

// GuardRouter declares a perm (HELL-280) so the MCP tool surfaces it.
type GuardRouter struct{}

func (GuardRouter) Act(ctx *rpc.Context) (string, error) { return "ok", nil }
func (GuardRouter) AuthzRequirements() map[string]string {
	return map[string]string{"act": "pages:write"}
}

// ---- helpers --------------------------------------------------------------

func mcpPost(t *testing.T, gw *gateway.Gateway, bearer, method string, params any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	hdr := gateway.Header{}
	if bearer != "" {
		hdr.Set("Authorization", "Bearer "+bearer)
	}
	resp := gw.Handle(context.Background(), &gateway.Request{
		Method: http.MethodPost, Path: "/mcp", Header: hdr, Body: body,
	})
	if resp.Status != http.StatusOK {
		t.Fatalf("%s: HTTP status = %d, body = %s", method, resp.Status, resp.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		t.Fatalf("%s: body not JSON: %s", method, resp.Body)
	}
	return out
}

func newMCPGateway() *gateway.Gateway {
	gw := gateway.New()
	gw.RegisterAuth(&AuthRouter{})
	gw.Register(&WhoRouter{})
	gw.Register(&GuardRouter{})
	gw.MustUse(mcp.New(mcp.Config{Version: "test"}))
	return gw
}

// The declared perm (HELL-280) rides into the MCP tool description, so the
// model knows the tool needs it — and tools/call enforces it via gw.Handle.
func TestMCP_ToolDescriptionCarriesPerm(t *testing.T) {
	gw := newMCPGateway()
	out := mcpPost(t, gw, "", "tools/list", map[string]any{})
	res, _ := out["result"].(map[string]any)
	tools, _ := res["tools"].([]any)
	for _, tv := range tools {
		tm, _ := tv.(map[string]any)
		if tm["name"] == "Guard.act" {
			if !strings.Contains(tm["description"].(string), "pages:write") {
				t.Fatalf("Guard.act description missing perm: %v", tm["description"])
			}
			return
		}
	}
	t.Fatalf("Guard.act tool not found: %v", tools)
}

// ---- tests ----------------------------------------------------------------

func TestMCP_Initialize(t *testing.T) {
	gw := newMCPGateway()
	out := mcpPost(t, gw, "", "initialize", map[string]any{})
	res, _ := out["result"].(map[string]any)
	if res["protocolVersion"] == nil {
		t.Fatalf("initialize missing protocolVersion: %v", out)
	}
	si, _ := res["serverInfo"].(map[string]any)
	if si["version"] != "test" {
		t.Fatalf("serverInfo.version = %v, want test", si["version"])
	}
}

func TestMCP_ToolsListReflectsRouters(t *testing.T) {
	gw := newMCPGateway()
	out := mcpPost(t, gw, "", "tools/list", map[string]any{})
	res, _ := out["result"].(map[string]any)
	tools, _ := res["tools"].([]any)
	var found bool
	for _, tv := range tools {
		tm, _ := tv.(map[string]any)
		if tm["name"] == "Who.me" {
			found = true
			if _, ok := tm["inputSchema"].(map[string]any); !ok {
				t.Fatalf("Who.me tool missing inputSchema: %v", tm)
			}
		}
	}
	if !found {
		t.Fatalf("tools/list did not reflect Who.me: %v", tools)
	}
}

// tools/call re-dispatches through gw.Handle, so the LLM's bearer resolves an
// identity and the handler sees it — MCP rides the RPC auth chain.
func TestMCP_ToolsCallRidesAuth(t *testing.T) {
	gw := newMCPGateway()

	ok := mcpPost(t, gw, "good-alice", "tools/call", map[string]any{"name": "Who.me", "arguments": map[string]any{}})
	res, _ := ok["result"].(map[string]any)
	if res["isError"] == true {
		t.Fatalf("authenticated tools/call flagged error: %v", res)
	}
	content, _ := res["content"].([]any)
	first, _ := content[0].(map[string]any)
	if !strings.Contains(first["text"].(string), "u_alice") {
		t.Fatalf("tool result missing subject: %v", res)
	}

	// Anonymous: the handler's RequireSubject 401s, surfaced as an MCP error.
	anon := mcpPost(t, gw, "", "tools/call", map[string]any{"name": "Who.me", "arguments": map[string]any{}})
	ares, _ := anon["result"].(map[string]any)
	if ares["isError"] != true {
		t.Fatalf("anonymous tools/call should be isError=true: %v", ares)
	}
}
