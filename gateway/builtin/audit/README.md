# `audit` plugin

Records every dispatch event into a sliding-window in-memory ring AND emits structured JSON to a writer (typically `os.Stdout` or a log file). Also exposes `Audit.recent` as a wire-callable RPC so operators can query recent events without scraping logs — demonstrates the plugin-as-also-a-service pattern. The Go type is `AuditRouter` so the engine auto-registers the `Audit` router.

## Hooks

- `DispatchHook` — per-request: writes one JSON line to `Out`, appends to the in-memory ring (overwriting oldest at capacity).
- `IntrospectContributor` — annotates this plugin's `PluginInfo.Extra` in `/rpc/_introspect.plugins[]` with `ring_count`, `ring_cap`, and `dropped`. Decoration only; ignores ctx/trace/visited.
- RPC methods — `Audit.recent` (POST `/rpc/Audit/recent`) returns newest-first events up to `Limit` (default 50). `PublicMethods()` declares `recent` as public; gate it via your authz policy in production.

## Constructor

`audit.New(audit.Config{...}) *AuditRouter`

## Config

| Field | Type | Default | Purpose |
|---|---|---|---|
| `Out` | `io.Writer` | — | Per-event JSON sink. Pass `io.Discard` to silence log emission and use the ring only. `nil` skips writes entirely. Blocking writers block the dispatch goroutine. |
| `RingSize` | `int` | `100` | In-memory ring capacity (events). Values `<= 0` fall back to the default. |

## Capabilities published

- `audit.Recent` — `RecentFn func(limit int) []gateway.DispatchEvent`. In-process ring reader; lookup via `gateway.GetCapability[audit.RecentFn](gw, "audit.Recent")`.

## Example

```go
import (
    "os"
    "github.com/Toyz/sov/gateway/builtin/audit"
)

gw.Use(audit.New(audit.Config{Out: os.Stdout, RingSize: 500}))
```
