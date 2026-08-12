# mcp

Model Context Protocol server built-in. Exposes the gateway's registered
routers as MCP tools — the same router struct that serves RPC and mesh now
serves MCP. This is the MCP arm of PEMM.

```go
gw.Use(mcp.New(mcp.Config{Version: build.Version}))
```

- **Endpoint:** `POST /mcp` (configurable via `Config.Path`), JSON-RPC 2.0
  over Streamable-HTTP (single-JSON reply).
- **Methods:** `initialize`, `tools/list`, `tools/call`, `ping`.
- **tools/list** is reflected from `engine.Describe()`: tool name
  `Router.method`, JSON Schema from the method's params (`ParamField.SchemaType`
  is already OpenAPI-shaped), description from the sov metadata plus the
  declared perm. Hard- and soft-hidden methods are excluded.
- **tools/call** re-dispatches through `gw.Handle`, so auth, authz, and the
  declarative perm (HELL-280) gate an MCP call **exactly** as they gate the
  same method over `/rpc`. There is no separate MCP capability model — MCP
  rides the perm system. The MCP client's bearer is the caller's identity.

## Customizing the tools

A router implements the optional `MCPToolProvider` marker to override how its
methods surface as tools — rename, re-describe, exclude, or expose an
otherwise-hidden method as an MCP-only tool:

```go
func (r *PageRouter) MCPTools() []mcp.MCPTool {
    return []mcp.MCPTool{
        {Method: "get",    Name: "read_page", Description: "Read a page for the model"},
        {Method: "export", Name: "export_page"},      // hard-hidden from /rpc, tool here
        {Method: "purge",  Exclude: true},            // never an MCP tool
    }
}
```

Specs are per-method **overrides**, not an allow-list: a method with no spec
still auto-reflects. A spec **explicitly includes** its method even if it is
hard/soft hidden from `/rpc` introspection — so you can keep a method off the
RPC surface and still offer it as an MCP-only tool. Renamed tools resolve back
to their method on `tools/call`, and the perm/authz gate still applies.

## Follow-ups
SSE streaming tools (via `PipeStream`), resources, prompts, a method-level doc
tag for richer descriptions, per-tool schema/description overrides, and
identity-scoped tool lists.
