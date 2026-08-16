# Changelog

All notable changes to sov are recorded here. Format follows
[Keep a Changelog](https://keepachangelog.com); versioning follows
[semver](https://semver.org). See [docs/VERSIONING.md](docs/VERSIONING.md) for
what "compatible" means.

## [Unreleased] — 1.0 hardening

The `feat/header-params` line is the intended 1.0. This wave hardens the whole
framework and adds the capabilities a production 1.0 needs. All changes are
stdlib-only (no external dependencies) and verified against live monolith + mesh
examples, not just unit tests.

### Added
- **Header-bound params** — `sov:"header=X-Tenant-Id"` binds a struct field from
  a request header (pre-parser snapshot, consistent with the authz gate).
- **Circuit breaker** on remote dispatch — per-upstream, opens after repeated
  transport **or** 5xx failures, half-open probe on cooldown. Optional
  **rate-based outlier ejection** (`FailureRateThreshold` over a rolling window)
  ejects a replica that fails intermittently, which the consecutive-count trip
  alone never catches; a recovery probe resets the window.
- **Graceful mesh deregister** on shutdown (`sov.ShutdownContext()`), plus
  readiness/liveness split (`/rpc/_ready`, serving-state, `ReadinessContributor`).
- **ratelimit** builtin (token bucket, pluggable `Limiter` backend).
- **accesslog** builtin; **profiler** builtin (pprof at `/debug/pprof`, auth-gated).
- **Metrics gauges** — in-flight requests, per-upstream breaker state, Go runtime.
- **build-info** on `/rpc/_manifest`; **RemoteIP** + **RequestID** on `DispatchEvent`.
- **OpenAPI 3.0** generator (`sov gen openapi`); **`sov catalog` snapshot/diff**
  backward-compat guard.
- **Request.RawQuery**; **Request.Host** (net/http lifts Host out of the header
  map, so it was invisible to plugins — now first-class for vhost/tenant routing
  and audit); MCP `tools/list` auth gate; scaffold ships tests + CI + Dockerfiles.
- Docs: `SECURITY.md`, `VERSIONING.md`, `ROADMAP_1.0.md`.
- **Multi-endpoint replicas** — a second pod registering an existing service name
  is now a load-balanced **replica** (default round-robin, breaker-aware — an open
  replica is skipped, all-open falls back), not a 409. Gives mesh services HA +
  failover. Pluggable `EndpointPicker` (`WithEndpointPicker`) for custom strategies.
- **Load shedding** — `WithMaxInFlight(n)` sheds past N concurrent requests with a
  retryable `503 OVERLOADED`. Adds the previously-missing `WithRemoteBreaker`,
  `WithAuthCacheTTL`, and `WithMaxInFlight` option setters.
- **Idempotency-aware retries** (`WithRetries`, off by default) — a failed remote
  dispatch re-resolves onto a different replica with exponential-full-jitter
  backoff (deadline-capped). A provably-never-executed failure (breaker open /
  dial refused) retries unconditionally; an ambiguous failure or upstream 5xx
  retries only under an `Idempotency-Key`, so non-idempotent ops are never
  silently re-sent.
- **Per-call deadline budget** (`deadline` builtin; `X-Sov-Deadline` shared across a
  mesh chain), **W3C traceparent** propagation (`tracing` builtin), and
  **Idempotency-Key** replay (`idempotency` builtin) — all with pluggable stores.
- **Pagination** primitives (`rpc.Page[T]` / `PageParams`); field **constraints**
  (`maxlen`), inbound JSON **depth cap**, and an **error taxonomy**
  (`retryable` / `retry_after` / per-field `details`).
- **First-class server-streaming RPC** — a method returning `(rpc.Stream[T],
  error)` streams results as NDJSON (one JSON item per line), backed by a Go 1.23
  `iter.Seq[T]` pulled lazily so unbounded sources stream in constant memory.
  Works locally and streams THROUGH a mesh hop unbuffered. The TypeScript client
  generates an `AsyncIterable<T>` consumer (`for await`); the OpenAPI response is
  documented as `application/x-ndjson`. Bidirectional streaming is a non-goal.
- **Opt-in `/rpc/_config`** — sanitized runtime-config dump (`configdump` builtin).
- Generated **TypeScript client**: per-call timeout, `X-Sov-Request-Id` capture,
  and the retry signal — parity with the Go/Python/Swift/Kotlin clients.

### Changed
- **BREAKING (`RegisterStore`)** — the pluggable registry store now holds replicas:
  `Delete(service, address string)` (was `Delete(service)`),
  `Snapshot() map[string][]RegisterEntry` (was `map[string]RegisterEntry`), and
  `Put` upserts by `(service, entry.Address)`. Custom stores (e.g. Redis) must
  update; the in-memory default, the `/rpc/_register` wire contract, and the
  `Resolve`/`PutEntry` signatures are unchanged. Entries are keyed by the
  canonical address, so equivalent URLs dedup to one replica.
- Auth/authz **denials are now recorded** as dispatch events (audit + metrics
  previously blind to 401/403 on the /rpc surface).
- All builtin constructors are variadic `New(...Config)`; `cmd/sov` subcommands
  moved under `internal/`; assorted API de-stuttering and doc fixes.
- `NetHTTPOptions.TrustProxyHeaders` (default off) gates X-Forwarded-For; a
  default `ReadTimeout` closes the slowloris body variant.

### Fixed
- **DoS**: a huge positional index in a `sov:` tag (`name,99999999999999`) sized
  a slot array to a multi-terabyte allocation and crashed `Register`; now bounded
  (found by a new tag-parser fuzz target).
- Handler panics are contained at the dispatch seam (no process crash).
- Disclosure endpoints (explorer/manifest) auth-gated by default; registry
  refuses to boot with an open `/rpc/_register`; meshsecret rejects exact replays.
- `sov init` monolith/hybrid scaffold shipped a boot-panicking handler
  (value param); README quickstart 404'd (missing surface); `gw.Use(slog.Default())`
  was wrongly rejected.
