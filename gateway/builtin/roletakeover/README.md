# `roletakeover` plugin

Relaxes the default cross-name role-binding guard. By default the gateway returns `409 ROLE_CONFLICT` when one pod tries to claim the Auth/Authz role another pod already holds; with this plugin registered, the most recent `_register` wins. Useful for blue-green deploys where the new auth pod legitimately needs to replace the old. Registered name: `role-takeover`.

The plugin owns the decision via `MeshConflictPolicy` (role case — `Conflict.Role` non-zero); the framework holds no role-takeover state.

## Hooks

- `MeshConflictPolicy` — `AllowMeshConflict` returns true whenever `Conflict.Role != 0` (role-takeover case). The plugin's mere presence IS the policy; drop the plugin to restore the 409 default. Returns false for federation conflicts so other plugins (e.g. `preempt`) get the chance. `ConsumeConflict` is a no-op.

## Constructor

`roletakeover.New(roletakeover.Config{...}) *Plugin`

## Config

| Field | Type | Default | Purpose |
|---|---|---|---|
| _(none)_ | — | — | Reserved for future role-scoped policies (e.g. allow takeover only for `RoleAuth`, not `RoleAuthz`). Empty struct keeps the uniform `New(Config{})` shape. |

## Example

```go
import "github.com/Toyz/sov/gateway/builtin/roletakeover"

gw.Use(roletakeover.New(roletakeover.Config{}))
```
