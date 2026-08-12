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

// NoteToolsRouter embeds mcp.Tool → its methods are MCP tools AND it still
// serves /rpc (normal *Router name). PEMM.
type NoteToolsRouter struct{ mcp.Tool }

type readParams struct {
	ID string `json:"id"`
}

func (NoteToolsRouter) Read(ctx *rpc.Context, p *readParams) (string, error) { return "note:" + p.ID, nil }

// PlainRouter does NOT embed mcp.Tool → never surfaces as a tool.
type PlainRouter struct{}

func (PlainRouter) Hello(ctx *rpc.Context) (string, error) { return "hi", nil }

// MixedToolsRouter hides one method: hidden methods are callable but not tools.
type MixedToolsRouter struct{ mcp.Tool }

func (MixedToolsRouter) Open(ctx *rpc.Context) (string, error) { return "open", nil }
func (MixedToolsRouter) Guts(ctx *rpc.Context) (string, error) { return "guts", nil }
func (MixedToolsRouter) HardHiddenMethods() []string           { return []string{"guts"} }

// GuardToolsRouter declares a perm → the tool description carries it.
type GuardToolsRouter struct{ mcp.Tool }

func (GuardToolsRouter) Act(ctx *rpc.Context) (string, error) { return "ok", nil }
func (GuardToolsRouter) AuthzRequirements() map[string]string {
	return map[string]string{"act": "pages:write"}
}

// Auth wiring so tools/call can resolve an identity.
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

type WhoToolsRouter struct{ mcp.Tool }

func (WhoToolsRouter) Me(ctx *rpc.Context) (string, error) { return rpc.RequireSubject(ctx) }

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

func toolNames(out map[string]any) map[string]bool {
	res, _ := out["result"].(map[string]any)
	tools, _ := res["tools"].([]any)
	names := map[string]bool{}
	for _, tv := range tools {
		tm, _ := tv.(map[string]any)
		if n, ok := tm["name"].(string); ok {
			names[n] = true
		}
	}
	return names
}

// ---- tests ----------------------------------------------------------------

func TestMCP_Initialize(t *testing.T) {
	gw := gateway.New()
	gw.MustUse(mcp.New(mcp.Config{Version: "test"}))
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

// Discovery is by the embedded marker: only mcp.Tool routers become tools; a
// plain router does not. The tool carries a reflected inputSchema.
func TestMCP_ToolsListFromMarker(t *testing.T) {
	gw := gateway.New()
	gw.Register(&NoteToolsRouter{})
	gw.Register(&PlainRouter{})
	gw.MustUse(mcp.New(mcp.Config{}))

	out := mcpPost(t, gw, "", "tools/list", map[string]any{})
	names := toolNames(out)
	if !names["NoteTools.read"] {
		t.Fatalf("NoteTools.read not surfaced: %v", names)
	}
	if names["Plain.hello"] {
		t.Fatalf("Plain.hello (no mcp.Tool) leaked as a tool: %v", names)
	}

	// The reflected inputSchema is present for the tool.
	res, _ := out["result"].(map[string]any)
	tools, _ := res["tools"].([]any)
	for _, tv := range tools {
		tm, _ := tv.(map[string]any)
		if tm["name"] == "NoteTools.read" {
			if _, ok := tm["inputSchema"].(map[string]any); !ok {
				t.Fatalf("NoteTools.read missing inputSchema: %v", tm)
			}
		}
	}
}

// Hidden methods on a tool router are callable but not tools.
func TestMCP_HiddenMethodNotTool(t *testing.T) {
	gw := gateway.New()
	gw.Register(&MixedToolsRouter{})
	gw.MustUse(mcp.New(mcp.Config{}))

	names := toolNames(mcpPost(t, gw, "", "tools/list", map[string]any{}))
	if !names["MixedTools.open"] {
		t.Fatalf("MixedTools.open should be a tool: %v", names)
	}
	if names["MixedTools.guts"] {
		t.Fatalf("hard-hidden MixedTools.guts should not be a tool: %v", names)
	}
}

// The declared perm (HELL-280) rides into the tool description.
func TestMCP_ToolDescriptionCarriesPerm(t *testing.T) {
	gw := gateway.New()
	gw.Register(&GuardToolsRouter{})
	gw.MustUse(mcp.New(mcp.Config{}))

	out := mcpPost(t, gw, "", "tools/list", map[string]any{})
	res, _ := out["result"].(map[string]any)
	tools, _ := res["tools"].([]any)
	for _, tv := range tools {
		tm, _ := tv.(map[string]any)
		if tm["name"] == "GuardTools.act" {
			if !strings.Contains(tm["description"].(string), "pages:write") {
				t.Fatalf("GuardTools.act description missing perm: %v", tm["description"])
			}
			return
		}
	}
	t.Fatalf("GuardTools.act tool not found: %v", tools)
}

// tools/call dispatches the named tool back to its method with its args.
func TestMCP_ToolCallDispatches(t *testing.T) {
	gw := gateway.New()
	gw.Register(&NoteToolsRouter{})
	gw.MustUse(mcp.New(mcp.Config{}))

	call := mcpPost(t, gw, "", "tools/call", map[string]any{
		"name": "NoteTools.read", "arguments": map[string]any{"id": "42"},
	})
	res, _ := call["result"].(map[string]any)
	content, _ := res["content"].([]any)
	first, _ := content[0].(map[string]any)
	if !strings.Contains(first["text"].(string), "note:42") {
		t.Fatalf("tool did not dispatch to Read with args: %v", res)
	}
}

// tools/call re-dispatches through gw.Handle, so the LLM's bearer resolves an
// identity and the handler sees it — MCP rides the RPC auth chain.
func TestMCP_ToolsCallRidesAuth(t *testing.T) {
	gw := gateway.New()
	gw.RegisterAuth(&AuthRouter{})
	gw.Register(&WhoToolsRouter{})
	gw.MustUse(mcp.New(mcp.Config{}))

	ok := mcpPost(t, gw, "good-alice", "tools/call", map[string]any{"name": "WhoTools.me", "arguments": map[string]any{}})
	res, _ := ok["result"].(map[string]any)
	if res["isError"] == true {
		t.Fatalf("authenticated tools/call flagged error: %v", res)
	}
	content, _ := res["content"].([]any)
	first, _ := content[0].(map[string]any)
	if !strings.Contains(first["text"].(string), "u_alice") {
		t.Fatalf("tool result missing subject: %v", res)
	}

	anon := mcpPost(t, gw, "", "tools/call", map[string]any{"name": "WhoTools.me", "arguments": map[string]any{}})
	ares, _ := anon["result"].(map[string]any)
	if ares["isError"] != true {
		t.Fatalf("anonymous tools/call should be isError=true: %v", ares)
	}
}
