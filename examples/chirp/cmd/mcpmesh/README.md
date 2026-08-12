# mcpmesh

One binary, two nodes — the SAME chirp code served over **both** `/rpc` and
**MCP**, meshed. Proof that a surface (MCP) meshes with no surface-specific mesh
code: it rides the same `Dispatch` fabric and federated introspect catalog that
`/rpc` already uses.

```
go run ./examples/chirp/cmd/mcpmesh
```

- **Node B** (`127.0.0.1:9101`) hosts the chirp services. `User`/`Chirp`/`Feed`
  embed `mcp.Tool`, so the same struct serves `/rpc` and is an MCP tool source.
- **Node A** (`127.0.0.1:9100`) is a thin edge — `registry` + `mcp` +
  `introspect`, **zero business routers**. It federates B and serves both
  surfaces; B enforces auth/authz when A proxies to it.

## What it proves (curl the edge, `:9100`)

```
# MCP discovery meshes: A lists tools that live on B
curl -s localhost:9100/mcp -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
#   -> Chirp.list/get/post/..., User.get/register/follow/..., Feed.timeline

# RPC meshes: proxied to B
curl -s localhost:9100/rpc/Auth/register -d '{"args":[{"handle":"zoe","password":"pw"}]}'
curl -s localhost:9100/rpc/Chirp/list    -d '{"args":[{"limit":50}]}'          # 200

# MCP dispatch meshes: routed to B (sees the same state RPC wrote)
curl -s localhost:9100/mcp -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"User.get","arguments":{"handle":"zoe"}}}'

# authz meshes on BOTH surfaces: anonymous write is denied by B, through A
curl -s localhost:9100/rpc/User/follow -d '{"args":[{"followee_id":"u_zoe"}]}'   # 401
curl -s localhost:9100/mcp -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"Chirp.post","arguments":{"body":"hi"}}}'  # isError
```

## The one trick

The tool wrappers embed the chirp router (methods promote) **and** `mcp.Tool`:

```go
type Chirp struct {
    *chirps.ChirpRouter
    mcp.Tool
}
```

The wrapper's type name is the wire name (`Chirp` — no `Router` suffix needed
anymore). That's all: the router now serves `/rpc` and MCP, locally or across
the mesh, with no other wiring.
