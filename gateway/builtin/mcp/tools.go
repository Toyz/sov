package mcp

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/rpc"
)

// MCPToolProvider is the optional marker a router implements to customize how
// its methods appear as MCP tools — rename them, re-describe them, exclude
// them, or expose an otherwise-hidden method as an MCP-only tool. A router
// that does NOT implement it falls back to auto-reflection (every non-hidden
// method becomes a tool named "Router.method").
//
// Specs are per-method OVERRIDES, not an allow-list: a method without a spec
// still auto-reflects. A spec's presence explicitly INCLUDES its method even
// if it is hard/soft hidden from /rpc introspection — so you can hard-hide a
// method from the RPC surface and still expose it as an MCP tool (an MCP-only
// tool). Regardless, tools/call rides the same auth/authz/perm as /rpc.
type MCPToolProvider interface {
	MCPTools() []MCPTool
}

// MCPTool overrides how one method surfaces as an MCP tool.
type MCPTool struct {
	Method      string // wire method name this tool invokes (required)
	Name        string // tool name override; default "Router.method"
	Description string // description override; default the method title + perm
	Exclude     bool   // omit this method from the MCP tool surface
}

// toolEntry is one resolved MCP tool: its wire identity + the router/method
// it dispatches to. Shared by tools/list (render) and tools/call (resolve).
type toolEntry struct {
	name        string
	router      string
	method      string
	description string
	schema      map[string]any
}

// toolEntries resolves every router+method into its MCP tool, applying any
// MCPToolProvider overrides. This is the single source of truth for both
// listing and calling, so a renamed tool resolves back to its method.
func (p *Plugin) toolEntries() []toolEntry {
	eng := p.gw.Engine()
	var out []toolEntry
	for _, rd := range eng.Describe() {
		specs := map[string]MCPTool{}
		if rv, ok := eng.RouterValue(rd.Router); ok {
			if prov, ok := rv.(MCPToolProvider); ok {
				for _, s := range prov.MCPTools() {
					specs[s.Method] = s
				}
			}
		}
		for _, md := range rd.Methods {
			spec, listed := specs[md.Method]
			switch {
			case listed && spec.Exclude:
				continue
			case listed:
				// explicit include — overrides the hidden check below
			case md.HardHidden || md.Internal:
				continue // auto: hidden methods are not tools
			}
			name := rd.Router + "." + md.Method
			if spec.Name != "" {
				name = spec.Name
			}
			desc := toolDescription(rd, md)
			if spec.Description != "" {
				desc = spec.Description
			}
			out = append(out, toolEntry{
				name:        name,
				router:      rd.Router,
				method:      md.Method,
				description: desc,
				schema:      inputSchema(md),
			})
		}
	}
	return out
}

// listTools renders the tool index for tools/list.
func (p *Plugin) listTools() []map[string]any {
	entries := p.toolEntries()
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

// callTool resolves the (possibly renamed) tool name to its router/method and
// dispatches through gw.Handle — so auth, authz, and the declarative perm
// gate the call exactly as they do over /rpc. The MCP request's bearer rides
// along, so the caller's identity is the LLM's identity.
func (p *Plugin) callTool(ctx context.Context, mcpReq *gateway.Request, params json.RawMessage) (map[string]any, *jsonRPCError) {
	var tc toolCallParams
	if err := json.Unmarshal(params, &tc); err != nil {
		return nil, &jsonRPCError{code: -32602, msg: "invalid params"}
	}
	var router, method string
	for _, e := range p.toolEntries() {
		if e.name == tc.Name {
			router, method = e.router, e.method
			break
		}
	}
	if router == "" {
		return nil, &jsonRPCError{code: -32602, msg: "unknown tool: " + tc.Name}
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
		Path:     "/rpc/" + router + "/" + method,
		Header:   hdr,
		Body:     body,
		RemoteIP: mcpReq.RemoteIP,
	}
	return toolResult(p.gw.Handle(ctx, sub)), nil
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
