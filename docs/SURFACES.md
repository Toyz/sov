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

## Quickstart — one router, both surfaces

A complete server. One router struct, served over `/rpc` **and** MCP, because you
register both surface builtins and the router opts into each with a marker.

```go
package main

import (
	"context"
	"log"

	"github.com/Toyz/sov"
	"github.com/Toyz/sov/gateway/builtin/mcp"
	"github.com/Toyz/sov/gateway/builtin/rpc"
)

// A router. Embed the surface markers it should appear on:
//   rpc.Served -> served over /rpc/{router}/{method}
//   mcp.Tool   -> exposed as an MCP tool
type NotesRouter struct {
	rpc.Served
	mcp.Tool
}

type GetParams struct {
	ID string `json:"id"`
}

func (NotesRouter) Get(_ *sov.Context, p *GetParams) (map[string]string, error) {
	return map[string]string{"id": p.ID, "body": "note " + p.ID}, nil
}

func main() {
	gw := sov.New()
	gw.MustUse(rpc.New())             // the /rpc surface (a builtin)
	gw.MustUse(mcp.New(mcp.Config{})) // the MCP surface (a builtin)
	gw.Register(&NotesRouter{})       // one struct, both surfaces
	log.Fatal(gw.ListenAndServe(context.Background(), ":8080"))
}
```

Hit both surfaces:

```sh
# RPC
curl -s localhost:8080/rpc/Notes/get -d '{"args":[{"id":"1"}]}'
#   -> {"data":{"id":"1","body":"note 1"}}

# MCP: list tools, then call one
curl -s localhost:8080/mcp -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
#   -> ... "name":"Notes.get" ...
curl -s localhost:8080/mcp -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"Notes.get","arguments":{"id":"1"}}}'
#   -> ... "text":"{\"data\":{\"id\":\"1\",\"body\":\"note 1\"}}" ...
```

That's the whole model: **a surface is a builtin you `Use`; a router opts into a
surface with a marker.** Notes on each:

- **`rpc.Served` is optional today** — `rpc.New()` serves every registered router,
  marker or not. It becomes required under `rpc.New(rpc.Config{RequireMarker: true})`,
  and the unmarked flow is deprecated in favor of the marker. `mcp.Tool` is always
  required (MCP is selective).
- **Presets do the wiring for you** — `sov.NewMonolith(...)`, `NewPod`, `NewRegistry`,
  `NewHybrid` already register `rpc` (plus registry, batch, cors, request-id). Reach
  for `sov.New()` + explicit `Use` only when you want to pick surfaces yourself.
- **An MCP-only node** is just this program without `gw.MustUse(rpc.New())` — no
  `/rpc`, tools still served.
- **Meshing is free** — see the runnable two-node example
  [`examples/chirp/cmd/mcpmesh`](../examples/chirp/cmd/mcpmesh/): the same routers,
  RPC + MCP, with the tool service on one node and the edge on another.

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

## The RPC surface is a builtin, exactly like MCP

`/rpc/{router}/{method}` is **not** hardcoded in core — it is a builtin plugin,
`gateway/builtin/rpc`, the same way `gateway/builtin/mcp` is the MCP surface. You
register it and the gateway auto-routes `/rpc` to it (a `RouteHandler` at
`"/rpc/"`, matched by longest-prefix so it never shadows `/rpc/_explorer/` or
framework endpoints):

```go
gw.Use(rpc.New())   // serve /rpc/{router}/{method}
gw.Use(mcp.New(...)) // serve /mcp — same shape
```

The presets (`NewMonolith`/`NewPod`/`NewRegistry`/`NewHybrid`) register `rpc` for
you, so preset-built gateways serve `/rpc` out of the box. A gateway that never
registers it simply doesn't speak `/rpc` — the `Dispatch` fabric still serves
other surfaces (MCP) over the same routers, so an **MCP-only node** is just a
gateway that Uses `mcp` and not `rpc`.

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
| Serve `/rpc` | `gw.Use(rpc.New())` (presets do this for you) |
| A node with no `/rpc` (MCP-only) | just don't Use `rpc` — Use `mcp` |
| Find routers by capability | `eng.Find(rpc.Implements[T]())` |
| Mesh a surface across nodes | edge runs `registry.New(...)`; `RegisterRemote(..., RemoteOptions{Introspect: true})` |
