# `upstreams` plugin

Limits which upstream gateway URLs a pod trusts `X-Sov-*` claim headers from. When `TrustUpstreamClaims` is enabled AND this plugin is registered, the trust guard strips inbound `X-Sov-*` from any caller whose `X-Sov-Upstream` header is not on the allowlist — the request proceeds as anonymous, not 401. Registered name: `upstream-gateways`.

The plugin owns the allowlist and the decision via `UpstreamTrustPolicy`; the framework holds no allowlist state.

## Hooks

- `UpstreamTrustPolicy` — `TrustUpstream` returns true iff the inbound `X-Sov-Upstream` header matches one of the allowlisted URLs (after normalization). The trust guard iterates registered policies; ALL must return true. Empty allowlist returns false; missing header returns false.

## Constructor

`upstreams.New(upstreams.Config{...}) *Plugin`

URLs are normalized via `gateway.NormalizeUpstreamURL`; invalid URLs panic at construction.

## Config

| Field | Type | Default | Purpose |
|---|---|---|---|
| `Allowed` | `[]string` | — | List of upstream gateway URLs whose `X-Sov-*` claim bundles will be trusted. |

## Example

```go
import "github.com/Toyz/sov/gateway/builtin/upstreams"

gw.Use(upstreams.New(upstreams.Config{
    Allowed: []string{"http://prime:8080", "http://edge:8080"},
}))
```
