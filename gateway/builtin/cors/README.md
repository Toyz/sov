# `cors` plugin

Adds CORS headers to every response and short-circuits the browser's `OPTIONS` preflight with a 204. Origin policy supports exact-match lists, a custom predicate, or the permissive default. When `Origins` includes `"*"`, credentials are auto-disabled (per spec).

## Hooks

- `HeaderParser` — intercepts `OPTIONS` requests from an allowed origin and returns the 204 preflight via a sentinel `*rpc.Error{Status:204,Code:"CORS_PREFLIGHT"}`; other methods pass through.
- `ResponseInterceptor` — adds `Access-Control-Allow-Origin`, `Vary: Origin`, and (when allowed) `Allow-Credentials` and `Expose-Headers` to every response from an allowed origin. Enriches the preflight 204 with `Allow-Methods`, `Allow-Headers`, and (optional) `Max-Age`.

## Constructor

`cors.New(cors.Config{...}) *Plugin`

## Config

| Field | Type | Default | Purpose |
|---|---|---|---|
| `Origins` | `[]string` | — | Exact-match origin list. `"*"` allows any and auto-disables credentials. |
| `OriginFunc` | `func(origin string) bool` | — | Custom predicate. When set, overrides `Origins`. |
| `AllowMethods` | `[]string` | `GET, POST, OPTIONS` | Preflight `Access-Control-Allow-Methods`. |
| `AllowHeaders` | `[]string` | `Content-Type, Authorization` | Preflight `Access-Control-Allow-Headers`. |
| `ExposeHeaders` | `[]string` | `X-Sov-Request-Id` | `Access-Control-Expose-Headers` on every response. |
| `AllowCredentials` | `bool` | `false` | Adds `Allow-Credentials: true`. Ignored when origin policy resolves to `"*"`. |
| `MaxAge` | `int` | `0` | Preflight cache seconds; `0` omits the header. |

Default (zero-value `Config`): any origin, no credentials, default method/header/expose lists.

## Requires / After

- Requires: `request-id` — the default `ExposeHeaders` lists `X-Sov-Request-Id`; without `request-id` the header would always be empty.
- After: `request-id` — soft hint so the response carries the id when this plugin runs.

## Example

```go
import "github.com/Toyz/sov/gateway/builtin/cors"

gw.Use(cors.New(cors.Config{
    Origins:          []string{"https://app.foo.io"},
    AllowCredentials: true,
    MaxAge:           600,
}))
```
