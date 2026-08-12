# rpc

The **RPC surface** builtin — the `/rpc/{router}/{method}` wire. It is a plugin
like every other, the exact counterpart of [`mcp`](../mcp/): both serve a surface
over the gateway's `Dispatch` mesh fabric; neither is hardcoded in core.

```go
gw.Use(rpc.New())   // serve /rpc/{router}/{method}
```

- **Route:** a `RouteHandler` mounted at `"/rpc/"`. The gateway routes matching
  paths to it by **longest-prefix** match, so it coexists with more-specific
  `/rpc/_*` plugin routes (explorer, batch) and a catch-all `"/"` SPA without
  shadowing them. Framework endpoints (`/rpc/_health`, `_introspect`, `_batch`,
  `_register`) are handled by core before plugin routes.
- **Behavior:** enforce `POST` + the reserved-name policy, then hand the call to
  `g.Dispatch` — which resolves it **local, to an in-process peer, or to a remote
  node** and returns the response. The surface itself knows nothing about
  local-vs-remote; that is the fabric's job.

## You register it — no magic

There is no core default and no import side-effect: a gateway serves `/rpc` iff
something registers this plugin. The presets
(`NewMonolith`/`NewPod`/`NewRegistry`/`NewHybrid`) register it for you, so
preset-built gateways serve `/rpc` out of the box. A gateway that never Uses it
simply doesn't speak `/rpc` — the `Dispatch` fabric still serves other surfaces
(MCP) over the same registered routers, so an **MCP-only node** is just a gateway
that Uses `mcp` and not `rpc`.

See [docs/SURFACES.md](../../../docs/SURFACES.md) for the full surfaces-over-the-mesh
model and a worked RPC + MCP mesh example.
