# `request-id` plugin

Stamps a stable `X-Sov-Request-Id` on every inbound request and propagates it through every hop the request takes: outbound proxy calls, in-process local dispatch, and downstream gateways. Idempotent: pre-existing `X-Sov-Request-Id` headers pass through unchanged so upstream-supplied ids (load balancer, edge gateway, browser tooling) propagate end-to-end.

Wire as the first `HeaderParser` so every downstream parser / translator / dispatch site already sees the id. Registered name: `request-id`.

## Hooks

- `HeaderParser` — generates the id at the inbound edge if no upstream caller set one.
- `HeaderInjector` — copies the id onto every outbound proxy `*http.Request`.
- `ContextContributor` — stashes the id on `rpc.Context.State` under `requestid.ContextKey` for in-process handlers. PEMM symmetry: monolith dispatch gets identical observability to mesh dispatch.
- `ResponseInterceptor` — echoes the id back on the response so clients can correlate logs by reading `X-Sov-Request-Id` off the envelope.

## Constructor

`requestid.New(requestid.Config{...}) *Plugin`

## Config

| Field | Type | Default | Purpose |
|---|---|---|---|
| `Generator` | `func() string` | 16-byte random hex | Overrides the default id generator (use UUIDv7 / ULID / snowflake by supplying a func). |

## Capabilities published

- `requestid.IDGenerator` — `IDGenerator func() string`. The plugin's own generator instance. Lookup via `gateway.GetCapability[requestid.IDGenerator](gw, "requestid.IDGenerator")` so peers (metrics, otlp, audit) can mint correlation ids matching the inbound scheme.

## Helpers

- `requestid.Header` — `"X-Sov-Request-Id"` wire name.
- `requestid.ContextKey` — `"sov.requestid"` state key.
- `requestid.FromContext(ctx)` — returns the stashed id (or `""`).

## Example

```go
import "github.com/Toyz/sov/gateway/builtin/requestid"

gw.Use(requestid.New(requestid.Config{}))
```
