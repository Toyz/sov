# `batch` plugin

Ships the cascading-batch endpoint at `/rpc/_batch`. The plugin owns the route; the framework holds zero batch state. Resolves each entry once, groups by destination, dispatches local/single entries through `gw.Handle`, and POSTs a single nested `/rpc/_batch` for remote groups of two or more. On 404 from a remote, falls back to per-entry dispatch and caches the negative answer.

## Hooks

- `ConfigApplier` — captures the `*gateway.Gateway` pointer at registration so `ServeRoute` can re-enter `gw.Handle` and read `gw.Resolver()`.
- `RouteHandler` — claims `/rpc/_batch` (POST only; other methods return 405).

## Constructor

`batch.New(batch.Config{...}) *Plugin`

## Config

| Field | Type | Default | Purpose |
|---|---|---|---|
| `UnsupportedTTL` | `time.Duration` | `60s` | Cache window for the "remote address returned 404 on `/rpc/_batch`" answer. Values `<= 0` fall back to the default. |

## Example

```go
import (
    "time"
    "github.com/Toyz/sov/gateway/builtin/batch"
)

gw.Use(batch.New(batch.Config{UnsupportedTTL: 2 * time.Minute}))
```
