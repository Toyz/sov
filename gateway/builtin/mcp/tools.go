package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/rpc"
)

// listTools reflects the registered routers into MCP tool definitions. Hard-
// and soft-hidden methods are excluded (they are not part of the tool
// surface). The tool name is "Router.method"; the JSON Schema comes from the
// method's params; the description comes from the sov metadata. Perm gating
// is NOT applied here — it applies at tools/call time via gw.Handle, so a
// method a caller cannot invoke is still listed (same as /rpc introspection).
func (p *Plugin) listTools() []map[string]any {
	tools := []map[string]any{}
	for _, rd := range p.gw.Engine().Describe() {
		for _, md := range rd.Methods {
			if md.HardHidden || md.Internal {
				continue
			}
			tools = append(tools, map[string]any{
				"name":        rd.Router + "." + md.Method,
				"description": toolDescription(rd, md),
				"inputSchema": inputSchema(md),
			})
		}
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

// callTool maps an MCP tools/call to the corresponding /rpc method and
// dispatches it through gw.Handle — so auth, authz, and the declarative perm
// gate the call exactly as they do over /rpc. The MCP request's bearer rides
// along on the sub-request, so the caller's identity is the LLM's identity.
func (p *Plugin) callTool(ctx context.Context, mcpReq *gateway.Request, params json.RawMessage) (map[string]any, *jsonRPCError) {
	var tc toolCallParams
	if err := json.Unmarshal(params, &tc); err != nil {
		return nil, &jsonRPCError{code: -32602, msg: "invalid params"}
	}
	router, method, ok := strings.Cut(tc.Name, ".")
	if !ok || router == "" || method == "" {
		return nil, &jsonRPCError{code: -32602, msg: "invalid tool name (want Router.method): " + tc.Name}
	}

	args := tc.Arguments
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	body, _ := json.Marshal(struct {
		Args json.RawMessage `json:"args"`
	}{Args: args})

	// Re-dispatch through the full gateway chain. Clone the MCP request's
	// header so the Authorization bearer (the LLM's identity) rides along;
	// pin JSON since the entry body we just built is JSON.
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
