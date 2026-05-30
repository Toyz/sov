# `meshsecret` plugin

HMAC gate for `/rpc/_register`. Pods joining the mesh sign their register POST with the same key the registry was constructed with; mismatches and replays (outside ±5 min) return `401`. Registered name: `mesh-secret`.

## Hooks

- `HeaderParser` — intercepts `POST /rpc/_register`, reads `X-Sov-Register-Sig` and `X-Sov-Register-Ts`, and verifies them against the request body using the configured secret. Other paths pass through.

## Constructor

`meshsecret.New(meshsecret.Config{...}) *Plugin`

## Config

| Field | Type | Default | Purpose |
|---|---|---|---|
| `Secret` | `[]byte` | — | HMAC key shared between the registry and every joining pod. Empty disables the gate. |

## Example

```go
import "github.com/Toyz/sov/gateway/builtin/meshsecret"

gw.Use(meshsecret.New(meshsecret.Config{Secret: secret}))
```
