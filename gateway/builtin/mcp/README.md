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

## Discovery — federated, via the registry filter engine

Each node tags its **local** tool routers in the introspect catalog through an
`IntrospectContributor` — it finds them with the engine's composable filter DSL:

```go
for _, ri := range engine.Find(rpc.Implements[mcp.ToolRouter]()) {
    // ri.Name is the wire name; ri.Value is the live instance
}
```

The filter DSL — `Find` with `Implements[T]()`, `ByName`, `ByTypeName`, and the
`And`/`Or`/`Not` combinators (plus the raw `Select(pred)` escape hatch) — lets
any surface builtin query the registry by capability, name, or type.

The tag lands on `rpc.RouterDescriptor.Surfaces` (`"mcp"`), which **federates**:
because it lives on the descriptor, a parent gateway that aggregates a node's
`/rpc/_introspect` inherits its surface tags. So `tools/list` is built from the
**federated** catalog (`IntrospectBody`), not just the local engine — node A
lists tools whose services actually run on node B, even though the `mcp.Tool`
marker (a local Go type) never crosses the wire. `tools/call` then routes to B
through the mesh `Dispatch` fabric. Meshed MCP with no MCP-specific mesh code.

## Auth / authz / mesh

`tools/call` routes through the gateway's **mesh fabric** — `Authorize` (the same
authz + declarative perm, HELL-280) then `Dispatch`. Because `Dispatch` resolves
the service local **or** remote, a tool whose service is federated to another
node just routes there with no MCP-specific code — the MCP surface meshes for
free. There is no separate MCP capability model — MCP rides the mesh. The MCP
client's bearer is the caller's identity; the declared perm also rides into each
tool's description so the model knows it's needed.

## Follow-ups

SSE streaming tools (via `PipeStream`), resources, prompts, a method-level doc
tag for richer descriptions, and identity-scoped tool lists.
