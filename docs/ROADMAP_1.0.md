# sov 1.0 gap register + build plan

Source: 5-dimension grounded audit (observability, resilience/mesh, protocol/data,
security depth, DX/testing/docs), 2026-08-16. Every item verified against the tree.
Ordered by dependency wave, not just priority — a wave's items share a seam or a
prerequisite. `[Pn]` priority, `[S/M/L]` effort.

Design guardrails (apply to every item): stdlib-only (no external deps), plugin-seam
where possible, "gaps are consumer responsibility" means sov ships the *seam*, not the
policy. Verify with live examples, not just `go test`.

## Wave 0 — foundational seams (unblock the rest)
- [x] **W0.1 Enrich `DispatchEvent` + record security denials** `[P0][S]` — add `RemoteIP`,
  `TraceID`, `SpanID`; emit an event on the auth/authz middleware short-circuit (401/403
  currently invisible to audit+metrics — they fire before `handle()`). Fixes the false
  SECURITY.md "RemoteIP stamped into audit events" claim. Unblocks W3.3 accesslog + W3.2
  tracing correlation. (security P0-1, obs #3/#6)
- [x] **W0.2 Cross-hop header contract** `[P0][M]` — one inbound-parse + outbound-inject
  point in `dispatchRemote`/`BuildProxyRequest` for `X-Sov-Deadline`, `traceparent`,
  `Idempotency-Key`. Shared plumbing for W1.2 + W3.2 + W2.5. (resilience #2, obs #2, protocol #5)

## Wave 1 — core resilience + mesh routing
- [ ] **W1.1 Multi-endpoint replicas + `EndpointPicker`** `[P0][L]` — KEYSTONE. Store becomes
  `[]RegisterEntry` per name; a 2nd address = replica, not 409. Default round-robin picker,
  breaker-aware (skip open). Unblocks retries/failover/outlier-ejection. (resilience #1)
- [x] **W1.2 Per-call deadline budget** `[P0][M]` — derive `context.WithTimeout` from a
  configurable default + inbound `X-Sov-Deadline`; stamp remaining budget on every hop so a
  chain shares one deadline. (resilience #2)
- [x] **W1.3 Breaker trips on 5xx** `[P1][S]` — currently only transport errors count; an
  up-but-broken pod (500s) never trips. Count 502/503/504. (resilience #4)
- [ ] **W1.4 Idempotency-aware retries** `[P1][M]` — bounded retry, exp backoff + full jitter,
  gated by an idempotency marker/key, re-pick a replica (W1.1), honor the budget (W1.2),
  retry budget to prevent storms. (resilience #3, #8)
- [x] **W1.5 Load shedding + transport tuning** `[P1][S-M]` — gateway/per-upstream in-flight
  semaphore → fast `503 OVERLOADED`; tuned `http.Transport` (MaxConnsPerHost, idle reuse).
  (resilience #5)
- [ ] **W1.6 Outlier ejection / hedging** `[P2][M]` — natural follow-on to W1.1+W1.3.

## Wave 2 — schema vocabulary / data-model completeness
- [x] **W2.1 Field validation constraints** `[P0][M]` — `min/max/minlen/maxlen/pattern/enum`
  in the tag grammar; enforce in a post-bind pass; surface to OpenAPI/MCP/codegen. (protocol #1)
- [x] **W2.2 Request caps** `[P0][S-M]` — `maxitems`/`maxlen` per field + global max array
  len / nesting depth guard in the decode path (decode-amplification DoS). (protocol #2, security P2-6)
- [x] **W2.3 Error taxonomy** `[P0][M]` — add `Retryable`, `Details []FieldError`, `RetryAfter`
  to `rpc.Error` + wire; populate Details from W2.1; thread into codegen `SovError` + OpenAPI;
  generate the `ErrorCode` union from registered codes (stop hardcoding). (protocol #3)
- [x] **W2.4 Pagination envelope** `[P1][M]` — optional `rpc.Page[T]` (`items,next_cursor,
  has_more`) + `PageParams`; codegen async-iterator. (protocol #4)
- [x] **W2.5 Idempotency-key + store seam** `[P1][M]` — `Idempotency-Key` header +
  `IdempotencyStore` iface (in-mem default), short-circuit replays. Pairs with W1.4. (protocol #5)
- [x] **W2.6 Method-level deprecation + Sunset** `[P1][S-M]` — `deprecated[=reason]` on the
  method sentinel; `MethodDescriptor.Deprecated`; emit to introspect/OpenAPI/codegen +
  optional `Sunset` header. (protocol #6)
- [ ] **W2.7 First-class server-streaming RPC + codegen** `[P1][L]` — DECISION NEEDED: promote
  streaming to a return kind `buildEntry` recognizes + codegen NDJSON consumers, or keep it
  surface-only. Bidi = explicit non-goal. (protocol #7)

## Wave 3 — observability / operability
- [x] **W3.1 Readiness vs liveness** `[P0][M]` — serving-state atomic (starting→ready→draining);
  `/rpc/_ready` 503 until ready + on drain; `ReadinessContributor` hook. (obs #1)
- [x] **W3.2 W3C traceparent builtin** `[P0-P1][M-L]` — parse/mint child span per hop
  (`requestid` is the template); set event trace fields (W0.1). (obs #2)
- [x] **W3.3 accesslog builtin** `[P0][S-M]` — one structured line per DispatchEvent with
  request-id; nothing logs on the dispatch path today. (obs #3)
- [x] **W3.4 pprof/debug builtin** `[P1][S]` — opt-in, auth-gated `net/http/pprof`. (obs #4)
- [x] **W3.5 build-info on the wire** `[P1][S]` — `debug.ReadBuildInfo` → manifest + a
  `build_info` gauge. (obs #5)
- [x] **W3.6 metric gaps** `[P1][S-M]` — in-flight gauge, `sov_upstream_breaker_state` gauge,
  go-runtime metrics. (obs #6)
- [x] **W3.7 runtime config dump** `[P2][S]` — opt-in `/rpc/_config` (sanitized). (obs #7)

## Wave 4 — security depth
- [x] **W4.1 Token cache TTL + revocation seam** `[P0][M]` — cap verify-cache lifetime
  independent of token expiry; refuse/warn on no-expiry tokens; `Revoked(token)` seam. (security P0-2)
- [x] **W4.2 TLS: fix broken escape hatch + real config** `[P1][M]` — `ListenAndServe` ignores
  a supplied `TLSConfig` (silently plaintext); add TLS via `ServeTLS`, `NetHTTPOptions.TLS`,
  mesh mTLS via proxy client `tls.Config`; correct SECURITY.md. (security P1-3)
- [x] **W4.3 HMAC key rotation** `[P1][M]` — `Secrets [][]byte`/kid; sign with primary, verify
  against all active; make-before-break rollover for meshsecret/hmacseal/registertoken. (security P1-4)
- [x] **W4.4 MCP tools/list gating** `[P2][S]` — optional `RequireAuthForList` so the perm map
  isn't anon-enumerable on an authed gateway. (security P2-7)
- [x] **W4.5 CORS default + CSRF stance** `[P2][S]` — flip zero-value CORS to same-origin;
  document bearer=CSRF-immune, cookie-auth=consumer-owned; optional Origin-check. (security P2-5)

## Wave 5 — DX / testing / docs / release
- [x] **W5.1 Fix README headline quickstart** `[P0][S]` — `sov.New()` mounts no surface →
  the copy-paste quickstart 404s. Fix docs (and decide whether `sov.New` should default-mount
  `rpc.New()`). (DX P0-1)
- [x] **W5.2 Generated-client resilience** `[P0][M]` — retry-on 429/503/network + backoff/jitter
  + `Idempotency-Key` + per-call timeout into every preamble; TS `call()` has NO timeout today;
  TS misses `X-Sov-Request-Id`. (DX P0-2, #11)
- [x] **W5.3 Release engineering** `[P0][S-M]` — `CHANGELOG.md`, `docs/VERSIONING.md` (stability
  guarantee: wire contract is the compat surface), tag process. (DX P0-3)
- [x] **W5.4 Exported `gatewaytest` harness** `[P1][M]` — promote gateway spin-up (currently
  internal gwtest) so consumers test mesh routing/auth-across-hop. (DX P1-4)
- [x] **W5.5 Fuzz the sov tag parser** `[P1][S]` — `FuzzParseSovTag`; the flagged bug-hotspot
  is unfuzzed. (DX P1-5)
- [x] **W5.6 rpctest in chirp handlers** `[P1][S]` — the flagship test-ergonomics package is in
  no example; chirp handlers have no unit tests. (DX P1-6)
- [ ] **W5.7 `sov gen mock`** `[P1][M-L]` — DECISION NEEDED: emit a contract-verifiable mock pod
  from a catalog (the "free mock" thesis is aspirational today). (DX P1-7)
- [x] **W5.8 Wire-compat guard** `[P1][M]` — `sov catalog snapshot` + `diff` (golden catalog,
  CI fails on breaking delta) reusing `ShapeHash`. (DX P1-8)
- [x] **W5.9 Scaffold tests + CI + Dockerfiles** `[P1][S-M]` — `sov init` ships no `_test.go`,
  no CI; monolith/hybrid have no Dockerfile. (DX P1-9)
- [x] **W5.10 Hygiene docs** `[P2][S]` — CONTRIBUTING, ADR index, getting-started matching
  current API (three different "expose a router" patterns today). (DX #10)

## Judgment calls to confirm (don't sink L-effort blind)
- W2.7 first-class streaming (L) vs keep surface-only + document.
- W5.7 `sov gen mock` (M-L) — is the "free mock" a 1.0 selling point or later?
- W4.2 mesh mTLS scope — sov ships it, or "edge terminates + document"? (fixing the broken
  escape hatch + correcting the doc is unconditional either way.)
