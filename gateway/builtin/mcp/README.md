# mcp

Model Context Protocol server built-in. Exposes registered routers as MCP
tools — the same router struct that serves RPC and mesh now serves MCP. This is
the MCP arm of PEMM.

```go
gw.Use(mcp.New(mcp.Config{Version: build.Version}))
```

- **Endpoint:** `POST /mcp` (configurable via `Config.Path`), JSON-RPC 2.0
  over Streamable-HTTP (single-JSON reply).
- **Methods:** `initialize`, `tools/list`, `tools/call`, `ping`.

## Declaring tools — embed `mcp.Tool`

A router opts into the MCP surface by embedding `mcp.Tool`. That — and only
that — makes it a tool router:

```go
type NoteToolsRouter struct{ mcp.Tool }

func (NoteToolsRouter) Read(ctx *rpc.Context, p *ReadParams) (*Note, error) { ... }
func (NoteToolsRouter) Search(ctx *rpc.Context, p *SearchParams) ([]*Note, error) { ... }
```

`gw.Register(&NoteToolsRouter{})` and it serves **both** `/rpc` and MCP. Every
non-hidden method becomes a tool; the tool's name, JSON Schema, description, and
declared perm are all **reflected** from the same engine metadata that drives
`/rpc`. There is no string-keyed override map — to rename a tool, name the
method; to keep a method off the tool surface, hide it (`HiddenMethods` /
`HardHiddenMethods`) — it stays callable, just isn't a tool.

### Why embed instead of a name convention

The marker method on `mcp.Tool` is **unexported**, which does two jobs:

1. The rpc engine never sees it — `reflect` lists only exported methods, so
   `Register` doesn't try to turn it into an RPC handler. No reserved-name
   change, no boot ordering to get right.
2. `ToolRouter` can't be satisfied by accident — the only way to implement it
   is to embed `mcp.Tool`, so "is this a tool router" is a real type check, not
   a string suffix.

## Discovery — the registry filter

The plugin finds tool routers through the engine's capability filter, not a
hard-wired list:

```go
for _, b := range rpc.SelectAs[mcp.ToolRouter](engine) {
    // b.Name is the wire name; b.Router is the router as mcp.ToolRouter
}
```

`rpc.SelectAs[T]` (and the general `engine.Select(pred)`) query the registry by
interface or predicate. MCP is just the first consumer — any plugin can `Select`
the routers it cares about and wire them to its own hooks.

## Auth / authz

`tools/call` re-dispatches through `gw.Handle`, so auth, authz, and the
declarative perm (HELL-280) gate an MCP call **exactly** as they gate the same
method over `/rpc`. There is no separate MCP capability model — MCP rides the
perm system. The MCP client's bearer is the caller's identity; the declared
perm also rides into each tool's description so the model knows it's needed.

## Follow-ups

SSE streaming tools (via `PipeStream`), resources, prompts, a method-level doc
tag for richer descriptions, and identity-scoped tool lists.
