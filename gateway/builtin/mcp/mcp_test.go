package mcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/mcp"
	"github.com/Toyz/sov/rpc"
)

// recordHook is a DispatchHook that captures per-call events.
type recordHook struct {
	mu     sync.Mutex
	events []gateway.DispatchEvent
}

func (*recordHook) PluginName() string { return "recordhook" }
func (r *recordHook) OnDispatch(ev gateway.DispatchEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
	return nil
}
func (r *recordHook) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}
func (r *recordHook) get(router, method string) (gateway.DispatchEvent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ev := range r.events {
		if ev.Router == router && ev.Method == method {
			return ev, true
		}
	}
	return gateway.DispatchEvent{}, false
}

// denyAuthz refuses every request — to prove a denied MCP tool call is recorded.
type denyAuthz struct{}

func (denyAuthz) Check(_ *rpc.Context, _ *gateway.CheckParams) (*gateway.AuthzDecision, error) {
	return &gateway.AuthzDecision{Allow: false, Reason: "nope"}, nil
}

// MCP tools/call must emit a DispatchHook event per call — with the resolved
// router/method/status — so audit/metrics see it (and authz DENIALS) exactly as
// they see a /rpc call, not just an opaque /mcp POST.
func TestMCP_ToolCallEmitsDispatchEvent(t *testing.T) {
	gw := gateway.New()
	gw.Register(&NoteToolsRouter{})
	rec := &recordHook{}
	gw.MustUse(rec)
	gw.MustUse(mcp.New())
	mcpPost(t, gw, "", "tools/call", map[string]any{"name": "NoteTools.read", "arguments": map[string]any{"id": "9"}})
	// EXACTLY ONE event — the tool's — not a double (tool + generic /mcp).
	if n := rec.count(); n != 1 {
		t.Fatalf("tools/call must record exactly one event, got %d: %+v", n, rec.events)
	}
	if ev, ok := rec.get("NoteTools", "read"); !ok || ev.Status != http.StatusOK {
		t.Fatalf("expected 200 dispatch event for NoteTools.read: %+v", rec.events)
	}

	// authz-denied path is recorded too (the key forensic gap) — and still once.
	gwd := gateway.New()
	gwd.Register(&NoteToolsRouter{})
	gwd.RegisterAuthz(&denyAuthz{})
	recd := &recordHook{}
	gwd.MustUse(recd)
	gwd.MustUse(mcp.New())
	mcpPost(t, gwd, "", "tools/call", map[string]any{"name": "NoteTools.read", "arguments": map[string]any{"id": "9"}})
	if ev, ok := recd.get("NoteTools", "read"); !ok || ev.Status != http.StatusForbidden {
		t.Fatalf("denied tool call should record a 403 dispatch event: %+v", recd.events)
	}
	if n := recd.count(); n != 1 {
		t.Fatalf("denied tools/call must record exactly one event, got %d: %+v", n, recd.events)
	}

	// An unknown-tool probe is recorded (enumeration signal), not invisible.
	gwp := gateway.New()
	gwp.Register(&NoteToolsRouter{})
	recp := &recordHook{}
	gwp.MustUse(recp)
	gwp.MustUse(mcp.New())
	mcpPost(t, gwp, "", "tools/call", map[string]any{"name": "NoteTools.doesNotExist", "arguments": map[string]any{}})
	if ev, ok := recp.get("", "NoteTools.doesNotExist"); !ok || ev.Status != http.StatusNotFound {
		t.Fatalf("unknown-tool probe should record a 404 event with the probed name: %+v", recp.events)
	}
}

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

// An "_"-prefixed method is internal-network only — the /rpc surface 404s it, so
// MCP (also external) must not list or dispatch it. Here a mcp.Tool router gains
// a "_internal" wire method via rpc.Handle; it must not become a tool.
func TestMCP_InternalMethodNotTool(t *testing.T) {
	gw := gateway.New()
	gw.Register(&MixedToolsRouter{})
	rpc.HandleErr(gw.Engine(), "MixedTools", "_internal", func(_ *rpc.Context, _ *readParams) error { return nil })
	gw.MustUse(mcp.New())

	names := toolNames(mcpPost(t, gw, "", "tools/list", map[string]any{}))
	if names["MixedTools._internal"] {
		t.Fatalf("_-prefixed method leaked as a tool: %v", names)
	}
	if !names["MixedTools.open"] {
		t.Fatalf("normal method should still be a tool: %v", names)
	}
	// tools/call on the internal method resolves to no tool.
	call := mcpPost(t, gw, "", "tools/call", map[string]any{"name": "MixedTools._internal", "arguments": map[string]any{}})
	if _, ok := call["error"]; !ok {
		t.Fatalf("tools/call on _internal should be an unknown-tool error: %v", call)
	}
}

// An MCP-only node: it never registers the rpc builtin, so /rpc 404s, but the
// SAME registered router still serves as an MCP tool — MCP routes through the
// Dispatch fabric, which is independent of the /rpc surface. "rpc is just a
// surface" made concrete: don't Use it, and the node simply doesn't speak /rpc.
func TestMCP_NoRPCBuiltin_MCPStillServes(t *testing.T) {
	gw := gateway.New() // no rpc.New() -> no /rpc surface
	gw.Register(&NoteToolsRouter{})
	gw.MustUse(mcp.New(mcp.Config{}))

	// /rpc surface is off.
	resp := gw.Handle(context.Background(), &gateway.Request{
		Method: http.MethodPost, Path: "/rpc/NoteTools/read", Header: gateway.Header{}, Body: []byte(`{"args":[{"id":"1"}]}`),
	})
	if resp.Status != http.StatusNotFound {
		t.Fatalf("/rpc should 404 on an MCP-only node, got %d: %s", resp.Status, resp.Body)
	}

	// MCP still lists AND calls the tool, via the fabric.
	names := toolNames(mcpPost(t, gw, "", "tools/list", map[string]any{}))
	if !names["NoteTools.read"] {
		t.Fatalf("MCP tool missing on rpc-disabled node: %v", names)
	}
	call := mcpPost(t, gw, "", "tools/call", map[string]any{"name": "NoteTools.read", "arguments": map[string]any{"id": "1"}})
	res, _ := call["result"].(map[string]any)
	content, _ := res["content"].([]any)
	first, _ := content[0].(map[string]any)
	if !strings.Contains(first["text"].(string), "note:1") {
		t.Fatalf("MCP tool call failed on rpc-disabled node: %v", res)
	}
}
