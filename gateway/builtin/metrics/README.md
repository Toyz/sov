# `metrics` plugin

Prometheus-text-format metrics for sov. Zero external dependencies — emits exposition format by hand. Per-call counter + duration histogram, labelled by router/method/status/mode/error_code.

## Hooks

- `DispatchHook` — increments `sov_requests_total` and observes `sov_request_duration_seconds` per call.
- `RouteHandler` — serves `/metrics` (configurable) in Prometheus text format.
- `IntrospectContributor` — adds `requests_total`, `label_sets`, `cardinality_cap`, `overflowed` to the plugin's `extra` block. Decoration only; ignores ctx/trace/visited.
- `CapabilityProvider` — publishes `metrics.Snapshot` for in-process readers.
- `ConfigApplier` — binds the gateway for runtime logger lookup.
- `PluginDependency` — `Requires: ["request-id"]`.

## Constructor

`metrics.New(metrics.Config{...}) *Plugin`

## Config

| Field | Type | Default | Purpose |
|---|---|---|---|
| `Buckets` | `[]float64` | 11-bucket Prom default | histogram upper bounds (seconds) |
| `CardinalityCap` | `int` | `1024` | cap on unique label combinations; excess coalesces to `_overflow` |
| `ExposePath` | `string` | `/metrics` | route the exposition endpoint serves on |
| `Namespace` | `string` | `sov` | prepends every metric name |

## Capabilities published

- `metrics.Snapshot` (`func() *MetricsSnapshot`) — point-in-time copy of counters + histograms; useful for audit dashboards or other plugins.

## Requires / After

- Requires: `request-id`

## Example

```go
import "github.com/Toyz/sov/gateway/builtin/metrics"

gw.Use(metrics.New(metrics.Config{
    ExposePath: "/internal/metrics",
    CardinalityCap: 2048,
}))

// Read snapshot in-process:
snap, _ := gateway.GetCapability[metrics.Snapshot](gw, "metrics.Snapshot")
fmt.Println(snap().Counters)
```
