# `manifest` plugin

Emits the PEMM manifest of a running gateway — a single JSON document at `/rpc/_manifest` describing services, plugins (re-using `/rpc/_introspect.plugins`), role bindings, federated remotes, and the introspectable list. Operators consume one URL to see the deployment shape.

## Hooks

- `ConfigApplier` — captures the `*gateway.Gateway` pointer.
- `RouteHandler` — claims `/rpc/_manifest`. `ServeRoute` re-enters `/rpc/_introspect` to pull plugin info, and reads `gw.Resolver()`, `gw.RegisterResolver().AddressGroup()`, `gw.AuthBinding()`, `gw.AuthzBinding()` to build the report.

## Constructor

`manifest.New(manifest.Config{...}) *Plugin`

## Config

| Field | Type | Default | Purpose |
|---|---|---|---|
| _(none)_ | — | — | Reserved for future knobs (mount path override, redact list). Empty struct keeps the uniform `New(Config{})` shape. |

## Response shape

```json
{
  "services":        ["Auth", "Authz", "User", ...],
  "plugins":         [{"name": "registry", "hooks": [...]}, ...],
  "auth":            {"service": "Auth",  "method": "verify"},
  "authz":           {"service": "Authz", "method": "check"},
  "remotes":         {"http://team-feed:9100": ["Chirp", "Feed"]},
  "introspectables": ["Auth", "Authz", "Chirp", ...]
}
```

## Example

```go
import "github.com/Toyz/sov/gateway/builtin/manifest"

gw.Use(manifest.New(manifest.Config{}))
```
