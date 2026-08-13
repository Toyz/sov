package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/rpc"
)

// Tool is the zero-size marker a router EMBEDS to expose its methods as MCP
// tools:
//
//	type NoteToolsRouter struct{ mcp.Tool }
//	func (NoteToolsRouter) Read(ctx *rpc.Context, p *ReadParams) (*Note, error) { ... }
//
// Embedding it — and only embedding it — makes the router satisfy ToolRouter,
// so the MCP built-in discovers it through the registry filter
// (eng.Find(rpc.Implements[ToolRouter]())). The marker method is UNEXPORTED,
// which does two jobs at once: the rpc engine never sees it (reflect lists only
// exported methods, so Register does not try to make it an RPC handler), and no
// code outside this package can forge ToolRouter — the only way to satisfy it
// is to embed Tool. The router serves /rpc AND MCP from the same struct — PEMM.
type Tool struct{}

func (Tool) sovMCPTool() {}

// ToolRouter is the capability the MCP built-in filters the registry for
// (rpc.Implements[ToolRouter]()). Satisfied only by embedding Tool.
type ToolRouter interface{ sovMCPTool() }

// toolEntry is one resolved MCP tool: its wire identity plus the router/method
// it dispatches to. Shared by tools/list (render) and tools/call (resolve), so
// a tool always resolves back to the exact method it names.
type toolEntry struct {
	name        string
	router      string
	method      string
	description string
	schema      map[string]any
}

// toolEntries resolves every tool method into an MCP tool from the FEDERATED
// introspect catalog — so tools include services on remote nodes, not just the
// local engine. A service is a tool source when its descriptor carries the "mcp"
// surface tag (stamped by each node's ContributeIntrospect; see federate.go),
// which federates on the RouterDescriptor. The public catalog already strips
// hard-hidden and omits soft-internal methods, so what remains is the tool
// surface. Name, schema, perm, and description are reflected from the same
// descriptor that drives /rpc. Single source of truth for listing AND calling,
// so a tool always resolves back to the exact (service, method) it names —
// local or across the mesh.
func (p *Plugin) toolEntries(ctx context.Context) []toolEntry {
	report := p.gw.FederatedCatalog(ctx)
	if report == nil {
		return nil
	}
	names := make([]string, 0, len(report.Services))
	for name := range report.Services {
		names = append(names, name)
	}
	sort.Strings(names) // stable tool order across a map
	var out []toolEntry
	for _, name := range names {
		for _, rd := range report.Services[name] {
			if !rd.HasSurface(surfaceName) {
				continue
			}
			for _, md := range rd.Methods {
				// Methods whose wire name starts with "_" are internal-network
				// only — the /rpc surface 404s them, so MCP (also an external
				// surface) must not expose or dispatch them either.
				if len(md.Method) > 0 && md.Method[0] == '_' {
					continue
				}
				out = append(out, toolEntry{
					name:        toolName(rd.Router, md.Method),
					router:      rd.Router,
					method:      md.Method,
					description: toolDescription(rd, md),
					schema:      inputSchema(md),
				})
			}
		}
	}
	return out
}

// toolName builds the tool's wire name. Centralized so the naming policy lives
// in one place: "Router.method" today.
func toolName(router, method string) string { return router + "." + method }

// listTools renders the tool index for tools/list.
func (p *Plugin) listTools(ctx context.Context) []map[string]any {
	entries := p.toolEntries(ctx)
	tools := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		tools = append(tools, map[string]any{
			"name":        e.name,
			"description": e.description,
			"inputSchema": e.schema,
		})
	}
	return tools
}

// toolDescription builds an LLM-facing description from the method's title
// and, when declared, its authz requirement (so the model knows a perm is
// needed). sov has no method-level doc tag yet — the title is the best
// signal today.
func toolDescription(rd rpc.RouterDescriptor, md rpc.MethodDescriptor) string {
	desc := md.Title
	if desc == "" {
		desc = rd.Router + "." + md.Method
	}
	if md.Perm != "" {
		desc += " (requires perm: " + md.Perm + ")"
	}
	return desc
}

// inputSchema derives a JSON Schema object from a method's params. SchemaType
// is already OpenAPI-shaped (string|number|boolean|array|object), which is a
// JSON Schema type, so the mapping is direct.
func inputSchema(md rpc.MethodDescriptor) map[string]any {
	props := map[string]any{}
	var required []string
	for _, f := range md.Params {
		props[f.JSONName] = fieldSchema(f)
		if f.Required {
			required = append(required, f.JSONName)
		}
	}
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func fieldSchema(f rpc.ParamField) map[string]any {
	s := map[string]any{}
	if f.SchemaType != "" {
		s["type"] = f.SchemaType
	}
	if f.SchemaType == "array" && f.ElemType != "" {
		s["items"] = map[string]any{"type": f.ElemType}
	}
	if desc := f.Desc; desc != "" {
		s["description"] = desc
	} else if f.Doc != "" {
		s["description"] = f.Doc
	}
	if f.Example != "" {
		s["examples"] = []any{f.Example}
	}
	return s
}

// toolCallParams is the MCP tools/call params shape.
type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// callTool resolves the tool name to its router/method and routes the call
// through the gateway's MESH FABRIC: Authorize (same authz + declarative perm as
// /rpc) then Dispatch, which resolves the service local OR remote. Routing
// through Dispatch — rather than gw.Handle/`/rpc` — is what MESHES the tool: a
// tool whose service is federated to another node just proxies there, with no
// MCP-specific mesh code. Identity was already resolved on the /mcp request
// (mcpReq.User) by the auth middleware; the MCP client's bearer is the caller.
func (p *Plugin) callTool(ctx context.Context, mcpReq *gateway.Request, params json.RawMessage) (map[string]any, *jsonRPCError) {
	// Every tools/call records exactly ONE dispatch event on mcpReq (which
	// suppresses handle's generic /mcp event) — so audit/metrics see a tool call
	// (and its failures/denials/probes) with parity to a direct /rpc call, not a
	// double count and not an opaque /mcp 200.
	started := time.Now()
	var tc toolCallParams
	if err := json.Unmarshal(params, &tc); err != nil {
		p.gw.RecordDispatch(mcpReq, "", "", "/mcp/tools/call", &gateway.Response{Status: http.StatusBadRequest, Mode: gateway.ModePlugin}, started)
		return nil, &jsonRPCError{code: -32602, msg: "invalid params"}
	}
	var router, method string
	for _, e := range p.toolEntries(ctx) {
		if e.name == tc.Name {
			router, method = e.router, e.method
			break
		}
	}
	if router == "" {
		// Record the probe with the attempted name so tool-name enumeration is
		// visible in the audit trail, not indistinguishable from a tools/list.
		p.gw.RecordDispatch(mcpReq, "", tc.Name, "/mcp/tools/call", &gateway.Response{Status: http.StatusNotFound, Mode: gateway.ModePlugin}, started)
		return nil, &jsonRPCError{code: -32602, msg: "unknown tool: " + tc.Name}
	}
	path := "/rpc/" + router + "/" + method

	// Same authz/perm gate /rpc applies; claims were resolved on the /mcp
	// request by the auth middleware, so reuse them (no re-verify).
	claims, _ := mcpReq.User.(*gateway.Claims)
	if err := p.gw.Authorize(ctx, claims, router, method, mcpReq.Header); err != nil {
		resp := gateway.ErrorResponseFromAny(err)
		p.gw.RecordDispatch(mcpReq, router, method, path, resp, started)
		return toolResult(resp), nil
	}

	args := tc.Arguments
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	body, _ := json.Marshal(struct {
		Args json.RawMessage `json:"args"`
	}{Args: args})

	hdr := mcpReq.Header.Clone()
	hdr.Set("Content-Type", "application/json")
	sub := &gateway.Request{
		Method:   http.MethodPost,
		Path:     path,
		Header:   hdr,
		Body:     body,
		User:     mcpReq.User, // verified claims flow to the handler / remote hop
		RemoteIP: mcpReq.RemoteIP,
	}
	resp := p.gw.Dispatch(ctx, sub)
	// Record against mcpReq (the outer request handle sees), not sub, so the
	// generic /mcp event is suppressed; the resolved router/method/status ride
	// the one event this call produces.
	p.gw.RecordDispatch(mcpReq, router, method, path, resp, started)
	return toolResult(resp), nil
}

// toolResult wraps an RPC response as an MCP tool result. The raw
// {data}/{error} envelope becomes a text content block; a >= 400 status sets
// isError so the client/LLM sees the failure.
func toolResult(resp *gateway.Response) map[string]any {
	if resp == nil {
		return map[string]any{
			"isError": true,
			"content": []map[string]any{{"type": "text", "text": "nil response"}},
		}
	}
	res := map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(resp.Body)}},
	}
	if resp.Status >= 400 {
		res["isError"] = true
	}
	return res
}
