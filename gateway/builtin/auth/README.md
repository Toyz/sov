# `auth` plugin

Translates verified `Claims` into legacy-shape headers (`X-Forwarded-User`, `X-Forwarded-Scopes`, `X-Tenant-Id`) on every outbound proxy hop. Use when downstream services expect those header names instead of (or in addition to) the `X-Sov-*` bundle the gateway already injects. Anonymous requests (`claims == nil`) are skipped.

## Hooks

- `AuthTranslator` — fires once per inbound request after the auth middleware resolves `Claims`. Mutates `req.Header` with the configured legacy header names; the gateway forwards those headers on every subsequent proxy hop.

## Constructor

`auth.New(auth.Config{...}) *Plugin`

## Config

| Field | Type | Default | Purpose |
|---|---|---|---|
| `SubjectHeader` | `string` | `X-Forwarded-User` | Header carrying `Claims.Subject`. Set to `"-"` to disable this stamp. |
| `TenantHeader` | `string` | `X-Tenant-Id` | Header carrying `Claims.Extra["tenant"]` when present as a string. Set to `"-"` to disable. |
| `ScopesHeader` | `string` | `X-Forwarded-Scopes` | Header carrying comma-joined `Claims.Scopes`. Set to `"-"` to disable. |

## Example

```go
import "github.com/Toyz/sov/gateway/builtin/auth"

gw.Use(auth.New(auth.Config{}))            // defaults
gw.Use(auth.New(auth.Config{ScopesHeader: "-"})) // suppress scopes header
```
