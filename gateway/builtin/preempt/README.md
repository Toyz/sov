# `preempt` plugin

Permits a federated `_register` to take over a service name already claimed by a different address. Map keys are exact wire-service names; values are the normalized address allowed to claim each. One-way (no symmetric grant-back), boot-time configuration only, consumed on successful takeover — register the reverse direction if you want to flip back later. Registered name: `federation-preemption`.

The plugin owns the map + the decision via `MeshConflictPolicy` (federation case — `Conflict.FederatedAddrs` populated); the framework holds no preemption state.

## Hooks

- `MeshConflictPolicy` —
  - `AllowMeshConflict(svc, _, Conflict{FederatedAddrs:[old,new]})` returns true when the map approves `new` as the takeover address for `svc`. Returns false for role-takeover conflicts (delegates to other plugins like `roletakeover`).
  - `ConsumeConflict(svc, c)` drops the entry once applied, so the same rule cannot replace the owner again. No-op for role-takeover conflicts.

## Constructor

`preempt.New(preempt.Config{...}) *Plugin`

URLs are normalized via `gateway.NormalizeUpstreamURL`; invalid URLs panic at construction.

## Config

| Field | Type | Default | Purpose |
|---|---|---|---|
| `Rules` | `map[string]string` | — | Wire-service name → normalized address allowed to claim it. |

## Example

```go
import "github.com/Toyz/sov/gateway/builtin/preempt"

gw.Use(preempt.New(preempt.Config{
    Rules: map[string]string{
        "Chirp": "http://team-feed-v2:9100",
    },
}))
```
