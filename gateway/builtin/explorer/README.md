# `explorer` plugin

Mounts the embedded HTML browser at `/rpc/_explorer/` (configurable). Default off; production binaries opt-in explicitly. The plugin owns the route and renders by re-entering `gw.Handle` on `/rpc/_introspect` to pick up the catalog — no extra surface state.

## Hooks

- `ConfigApplier` — captures the `*gateway.Gateway` pointer for later introspect re-entry.
- `RouteHandler` — claims `<prefix>` and `<prefix>/` (subtree match). `ServeRoute` re-enters `/rpc/_introspect` and renders the embedded UI.

## Constructor

`explorer.New(explorer.Config{...}) *Plugin`

## Config

| Field | Type | Default | Purpose |
|---|---|---|---|
| `PathPrefix` | `string` | `/rpc/_explorer` | Mount prefix (leading slash required; trailing slash added for subtree match). |

## Example

```go
import "github.com/Toyz/sov/gateway/builtin/explorer"

gw.Use(explorer.New(explorer.Config{}))
gw.Use(explorer.New(explorer.Config{PathPrefix: "/admin/_ui"}))
```
