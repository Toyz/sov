# Surfaces over the mesh fabric

A **surface** is a protocol a client speaks to reach your registered routers —
`/rpc` is one, MCP is another. In sov the surfaces are decoupled from the
routing: the mesh **fabric** decides *where* a call goes (local, an in-process
peer, or a remote node), and each surface only has to translate its wire into a
canonical call and hand it to the fabric. So any surface meshes with no
surface-specific mesh code.

```
   client speaks /rpc ─┐                       ┌─ local engine (in-process)
   client speaks MCP ──┼─▶ g.Dispatch(Request) ─┼─ peer gateway (nested PEMM)
   (a future surface) ─┘        the fabric      └─ remote pod (HTTP proxy)
```

Three ideas, in order:

1. `g.Dispatch` — the fabric every surface routes through.
2. The **registry filter engine** — how a surface finds the routers it serves.
3. Federation — how a surface discovers routers that live on *other* nodes.

---

## 1. The fabric: `g.Dispatch`

```go
func (g *Gateway) Dispatch(ctx context.Context, req *Request) *Response
```

`req.Path` is `/rpc/{service}/{method}`; that carries the service + method.
`Dispatch` resolves the service and routes the call — local engine, in-process
peer, or remote proxy — and returns the response. **The caller never knows which.**
That is the whole point: a tool call, a batch entry, or a `/rpc` request all go
through the same seam, and "remote" is the resolver's job, not the surface's.

`Dispatch` does **not** run the auth/authz middleware (the `/rpc` HTTP path runs
that before it reaches the fabric). A surface reaching the fabric *outside* the
HTTP chain owns identity and calls `g.Authorize` first:

```go
func (g *Gateway) Authorize(ctx, claims *Claims, service, method string, headers Header) error
```

`Authorize` applies the same authz + declarative perm gate `/rpc` applies.

---

## The RPC surface is just a surface

`/rpc/{router}/{method}` is served by a **replaceable seam**, not hardcoded
routing. It defaults to the built-in surface (which hands off to `Dispatch`), so
every gateway serves `/rpc` out of the box. Two options change that:

```go
gateway.New(gateway.WithoutRPCSurface())   // node has NO /rpc; other surfaces still work
gateway.New(gateway.WithRPCSurface(h))      // custom wire format on /rpc, still over Dispatch
```

Because the fabric is independent of the surface, a node built
`WithoutRPCSurface()` still serves its registered routers over MCP — an
**MCP-only node** with no `/rpc` endpoint.

---

## Registering a router — no `Router` suffix required

A router registers under its own type name; a trailing `Router` is stripped only
as a convention for a clean wire name.

```go
type NoteTools struct{ /* ... */ }          // wire name: "NoteTools"
type NoteToolsRouter struct{ /* ... */ }     // wire name: "NoteTools" (suffix trimmed)
gw.Register(&NoteTools{})
```

---

## 2. The registry filter engine

A surface builtin doesn't hardcode a list of routers — it **queries** the
registry by capability, name, or type. The DSL is composable:

```go
// every router that implements interface T (by capability)
eng.Find(rpc.Implements[mcp.ToolRouter]())

// compose: capability AND a name predicate, negated, etc.
eng.Find(rpc.And(
    rpc.Implements[MyCapability](),
    rpc.Not(rpc.ByName(func(n string) bool { return strings.HasPrefix(n, "_") })),
))
```

| Builder | Matches |
|---|---|
| `rpc.Implements[T]()` | routers implementing interface `T` |
| `rpc.ByName(pred)` | wire name satisfies `pred` |
| `rpc.ByTypeName(pred)` | Go type name satisfies `pred` |
| `rpc.And/Or/Not(...)` | combine filters |
| `eng.Find(filter)` | run a filter, returns `[]RouterInfo` |
| `eng.Select(pred)` | raw predicate escape hatch |

`RouterInfo` carries `Name`, `TypeName`, and the live `Value` (so a predicate
can type-assert a marker interface). This is the seam any new surface uses to
decide what it serves.

---

## The MCP surface: `gw.Use(mcp.New(...))`

A router opts into the MCP surface by **embedding `mcp.Tool`**:

```go
type NoteTools struct {
    mcp.Tool                 // marker: this router's methods are MCP tools
    store *notes.Service
}

func (n *NoteTools) Read(ctx *rpc.Context, p *ReadParams) (*Note, error) { ... }
```

- The marker method on `mcp.Tool` is **unexported**, so the rpc engine never
  reflects it as a handler, and no code outside the mcp package can forge the
  capability — the only way to be a tool router is to embed `mcp.Tool`.
- The mcp builtin discovers tool routers with `Find(rpc.Implements[ToolRouter]())`,
  reflects each method into a tool (name, JSON Schema, description, declared
  perm — the same metadata that drives `/rpc`), and serves `POST /mcp`
  (JSON-RPC 2.0: `initialize`, `tools/list`, `tools/call`, `ping`).
- `tools/call` runs `Authorize` then `Dispatch` — so a tool call is gated by the
  same auth/authz/perm as `/rpc`, and routes local or remote.

The **same struct** now serves `/rpc` and MCP. That is PEMM across surfaces.

---

## 3. How meshing works — the federated `Surfaces` tag

The `mcp.Tool` marker is a local Go type; it never crosses the wire. So how does
node A list a tool whose service runs on node B? Through the introspect catalog.

`rpc.RouterDescriptor` carries a generic `Surfaces []string`. The engine never
sets it — a **surface builtin** stamps its own name (`"mcp"`) onto its local tool
routers via an `IntrospectContributor`. Because the tag lives on the descriptor,
it **federates**: when node A aggregates node B's `/rpc/_introspect`, it inherits
B's surface tags.

So the mcp builtin builds `tools/list` from the **federated** catalog
(`IntrospectBody`), selecting `"mcp"`-tagged services — local engine *plus* every
remote service the registry aggregator merged in:

```
node B (has NoteTools, mcp.Tool)         node A (edge: registry + mcp, no local tools)
  /rpc/_introspect  ── tagged "mcp" ──▶  aggregated catalog
                                          tools/list ─▶ NoteTools.read   (discovered)
                                          tools/call ─▶ Dispatch ─▶ proxy to B  (routed)
```

Node A discovers and calls B's tool, with **no MCP-specific mesh code** — it
reuses the introspect federation that `/rpc` already has, plus the `Dispatch`
fabric. Federated discovery needs the `registry` builtin (the aggregator) on the
edge and the remote registered introspectable
(`RegisterRemote(name, addr, ttl, gateway.RemoteOptions{Introspect: true})` or a
pod's `JoinMesh(..., Introspectable: true)`).

---

## Worked example

[`examples/chirp/cmd/mcpmesh`](../examples/chirp/cmd/mcpmesh/) — one binary, two
nodes, the same chirp code served over `/rpc` and MCP, meshed. Node B owns the
services (`User`/`Chirp`/`Feed` embed `mcp.Tool`); node A is a thin edge that
federates B and serves both surfaces. `go run ./examples/chirp/cmd/mcpmesh`, then
curl `:9100` — the README there has the exact commands and shows auth/authz being
enforced by B across the mesh on both surfaces.

---

## Quick reference

| Want | Do |
|---|---|
| Serve a router over `/rpc` | `gw.Register(&Foo{})` (default) |
| Serve it as an MCP tool too | embed `mcp.Tool`, `gw.Use(mcp.New(...))` |
| A node with no `/rpc` | `gateway.New(gateway.WithoutRPCSurface())` |
| A custom `/rpc` wire | `gateway.New(gateway.WithRPCSurface(h))` |
| Find routers by capability | `eng.Find(rpc.Implements[T]())` |
| Mesh a surface across nodes | edge runs `registry.New(...)`; `RegisterRemote(..., RemoteOptions{Introspect: true})` |
