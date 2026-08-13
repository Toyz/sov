// Package mcp serves a Model Context Protocol endpoint (default /mcp) that
// exposes registered routers as MCP tools — so any MCP client (Claude, Cursor,
// Copilot) can call your sov methods as tools. It is the MCP arm of PEMM: the
// SAME router struct serves RPC, mesh, AND MCP.
//
// A router opts into the MCP surface by embedding mcp.Tool (see tools.go). The
// plugin discovers those routers through the engine's capability filter
// (eng.Find(rpc.Implements[ToolRouter]())) — no name convention, no
// registration list. Each method of a tool router becomes a tool: name, JSON
// Schema, description, and declared perm are all REFLECTED from the same engine
// metadata that drives /rpc. tools/call routes through the gateway's mesh fabric
// (Authorize + Dispatch), so the full request chain — auth, authz, and the
// declarative perm (HELL-280) — gates an MCP call exactly as it gates the same
// method over /rpc, AND a tool whose service is federated to another node just
// routes there. There is no separate MCP capability model: MCP rides the mesh.
//
// Transport is Streamable-HTTP (JSON-RPC 2.0 over POST, single-JSON reply);
// SSE tools, resources, and prompts are follow-ups.
//
//	gw.Use(mcp.New(mcp.Config{Version: build.Version}))
package mcp

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/Toyz/sov/gateway"
)

const (
	defaultPath     = "/mcp"
	defaultName     = "sov"
	protocolVersion = "2025-06-18"
)

// Config configures the MCP plugin. Zero values are fine: Path defaults to
// /mcp and ServerName to "sov".
type Config struct {
	Path       string // endpoint path; default /mcp
	ServerName string // reported in serverInfo.name; default "sov"
	Version    string // reported in serverInfo.version
}

// Plugin is the MCP route owner returned by New.
type Plugin struct {
	gw      *gateway.Gateway
	path    string
	name    string
	version string
}

// Compile-time proof of the hooks this plugin binds.
var (
	_ gateway.Plugin        = (*Plugin)(nil)
	_ gateway.PluginDoc     = (*Plugin)(nil)
	_ gateway.ConfigApplier = (*Plugin)(nil)
	_ gateway.RouteHandler  = (*Plugin)(nil)
)

// New returns the MCP plugin.
//
//	gw.Use(mcp.New(mcp.Config{}))
func New(cfg ...Config) *Plugin {
	if len(cfg) > 1 {
		panic("mcp.New: at most one Config")
	}
	var c Config
	if len(cfg) == 1 {
		c = cfg[0]
	}
	p := &Plugin{path: c.Path, name: c.ServerName, version: c.Version}
	if p.path == "" {
		p.path = defaultPath
	}
	if p.name == "" {
		p.name = defaultName
	}
	return p
}

func (p *Plugin) PluginName() string { return "mcp" }

func (p *Plugin) Doc() string {
	return "Model Context Protocol server at " + p.path + " — exposes routers that embed mcp.Tool as MCP tools; tools/call rides the same auth/authz/perm as /rpc."
}

func (p *Plugin) Apply(g *gateway.Gateway) error { p.gw = g; return nil }

func (p *Plugin) RoutePatterns() []string { return []string{p.path} }

// jsonReq is one incoming JSON-RPC 2.0 message. A missing id = a notification.
type jsonReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

var jsonHeader = gateway.Header{"Content-Type": "application/json"}

// ServeRoute handles one JSON-RPC POST: parse, route the MCP method, envelope
// the reply. Auth is NOT enforced here — it is enforced per tools/call, which
// calls gw.Authorize then gw.Dispatch (and emits a dispatch event via
// RecordDispatch for audit/metrics), so listing is open but calling is gated —
// and observed — exactly like /rpc.
func (p *Plugin) ServeRoute(ctx context.Context, req *gateway.Request) *gateway.Response {
	if req.Method != http.MethodPost {
		return &gateway.Response{Status: http.StatusMethodNotAllowed, Header: gateway.Header{"Allow": "POST"}}
	}
	var rq jsonReq
	if err := json.Unmarshal(req.Body, &rq); err != nil {
		return jsonRPC(http.StatusOK, errBody(nil, -32700, "parse error"))
	}
	if len(rq.ID) == 0 {
		// Notification: acknowledge with no body.
		return &gateway.Response{Status: http.StatusAccepted}
	}

	switch rq.Method {
	case "initialize":
		return jsonRPC(http.StatusOK, okBody(rq.ID, map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": p.name, "version": p.version},
		}))
	case "tools/list":
		return jsonRPC(http.StatusOK, okBody(rq.ID, map[string]any{"tools": p.listTools(ctx)}))
	case "tools/call":
		res, jerr := p.callTool(ctx, req, rq.Params)
		if jerr != nil {
			return jsonRPC(http.StatusOK, errBody(rq.ID, jerr.code, jerr.msg))
		}
		return jsonRPC(http.StatusOK, okBody(rq.ID, res))
	case "ping":
		return jsonRPC(http.StatusOK, okBody(rq.ID, map[string]any{}))
	default:
		return jsonRPC(http.StatusOK, errBody(rq.ID, -32601, "method not found: "+rq.Method))
	}
}

// ---- JSON-RPC 2.0 enveloping ----------------------------------------------

type jsonRPCError struct {
	code int
	msg  string
}

func okBody(id json.RawMessage, result any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": rawOrNull(id), "result": result}
}

func errBody(id json.RawMessage, code int, msg string) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": rawOrNull(id), "error": map[string]any{"code": code, "message": msg}}
}

func rawOrNull(id json.RawMessage) any {
	if len(id) == 0 {
		return nil
	}
	return id
}

func jsonRPC(status int, body map[string]any) *gateway.Response {
	return &gateway.Response{Status: status, Header: jsonHeader, Body: mustJSON(body)}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		b, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32603, "message": "encode error"}})
	}
	return b
}
