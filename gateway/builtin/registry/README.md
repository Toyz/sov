# `registry` plugin

Promotes the gateway to central-registry shape. Owns `POST /rpc/_register`, the public top-level `/health`, and the introspect/health aggregation that fans out across registered remotes. Without this plugin the gateway stays in pod shape: `/rpc/_register` returns 404 and the public `/health` is not routed.

Both role-binding conflicts AND federation conflicts defer to registered `MeshConflictPolicy` plugins (e.g. `roletakeover` for the role case, `preempt` for the federation case). The framework dispatches on the `Conflict` discriminator (`Conflict.Role` vs `Conflict.FederatedAddrs`).

## Hooks

- `ConfigApplier` — captures the `*gateway.Gateway` pointer.
- `RouteHandler` — claims `/rpc/_register` (full register handler: role binding + federation conflict + writes to the register resolver) and `/health` (public k8s probe; delegates to `/rpc/_health`).
- `IntrospectContributor` (`aggregator.go`) — fans out to every registered introspectable remote, dedupes via the visited-set, and merges descriptors into `report.Services`.
- `HealthAggregator` (`aggregator.go`) — probes every registered remote address once, dedupes per-address, populates `HealthService.Children` for tiered rollup, and translates HTTP status (`200/207/503/...`) into healthy/degraded/unhealthy/unknown.

## Constructor

`registry.New(registry.Config{...}) *Plugin`

## Config

| Field | Type | Default | Purpose |
|---|---|---|---|
| `IntrospectProbeTimeout` | `time.Duration` | `2s` | Per-address timeout when fetching a remote's `/rpc/_introspect`. Values `<= 0` fall back to the default. |
| `HealthProbeTimeout` | `time.Duration` | `1s` | Per-address timeout when probing a remote's `/rpc/_health`. Values `<= 0` fall back to the default. |

## Capabilities published

- `registry.AddressGroup` — `AddressGroupFn func() map[string][]string`. Returns the live `(address → []service)` map without reaching into the register resolver. Used by metrics + drift to know which addresses are live without re-implementing the cache. Lookup via `gateway.GetCapability[registry.AddressGroupFn](gw, "registry.AddressGroup")`.

## Example

```go
import (
    "time"
    "github.com/Toyz/sov/gateway/builtin/registry"
)

gw.Use(registry.New(registry.Config{
    IntrospectProbeTimeout: 3 * time.Second,
}))
```
