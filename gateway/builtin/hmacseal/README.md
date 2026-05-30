# `hmacseal` plugin

Seals injected `X-Sov-*` claim headers with HMAC-SHA256 so downstream services can detect forged claim bundles. Wears two hats: writes the seal on outbound proxy hops, and verifies the seal on inbound trust-guard checks. Register last so the seal covers headers written by earlier plugins. Registered name: `hmac-seal`.

## Hooks

- `HeaderInjector` — writes `X-Sov-Seal` across the `X-Sov-*` bundle on every outbound proxy hop. No-op when secret is empty or there is no `X-Sov-Subject` to seal.
- `SealVerifier` — returns true iff the inbound `X-Sov-Seal` header verifies under the plugin's secret. The trust guard iterates registered verifiers; first true wins.

## Constructor

`hmacseal.New(hmacseal.Config{...}) *Plugin`

## Config

| Field | Type | Default | Purpose |
|---|---|---|---|
| `Secret` | `[]byte` | — | HMAC key (32+ bytes recommended). Empty turns the plugin into a no-op (dev mode). |

## Capabilities published

- `hmacseal.SealKey` — `SealKeyFn func() []byte`. Returns a COPY of the seal secret (mutation-safe). Lookup via `gateway.GetCapability[hmacseal.SealKeyFn](gw, "hmacseal.SealKey")` for future crypto plugins that need to sign with the same key.

## Example

```go
import "github.com/Toyz/sov/gateway/builtin/hmacseal"

gw.Use(hmacseal.New(hmacseal.Config{Secret: secret}))
```
