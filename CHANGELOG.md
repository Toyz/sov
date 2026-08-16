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
  transport **or** 5xx failures, half-open probe on cooldown.
- **Graceful mesh deregister** on shutdown (`sov.ShutdownContext()`), plus
  readiness/liveness split (`/rpc/_ready`, serving-state, `ReadinessContributor`).
- **ratelimit** builtin (token bucket, pluggable `Limiter` backend).
- **accesslog** builtin; **profiler** builtin (pprof at `/debug/pprof`, auth-gated).
- **Metrics gauges** — in-flight requests, per-upstream breaker state, Go runtime.
- **build-info** on `/rpc/_manifest`; **RemoteIP** + **RequestID** on `DispatchEvent`.
- **OpenAPI 3.0** generator (`sov gen openapi`); **`sov catalog` snapshot/diff**
  backward-compat guard.
- **Request.RawQuery**; MCP `tools/list` auth gate; scaffold ships tests + CI +
  Dockerfiles.
- Docs: `SECURITY.md`, `VERSIONING.md`, `ROADMAP_1.0.md`.

### Changed
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
