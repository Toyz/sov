# Sov security posture (1.0)

Sov is a protocol-enforced modular monolith: the same router code runs as an
in-process monolith or a distributed mesh. Its security model follows one rule —
**verification confers no standing trust**. Every boundary re-checks; defaults
are closed; a component that cannot prove a claim treats it as absent, not as
permitted.

This document states what Sov guarantees by default, what it leaves to the
consumer, and the knobs that move the line.

## Secure-by-default

These hold with no configuration beyond registering the relevant builtin.

- **Inbound identity headers are stripped.** A gateway does not trust
  `X-Sov-Subject`, `X-Sov-*`, or any identity-claim header from the wire.
  `NetHTTPOptions.TrustUpstreamClaims` (default `false`) must be set — and
  should be paired with `hmacseal` — before a downstream pod honors claims
  injected by an upstream gateway. A public-facing gateway left at the default
  cannot be told "I am admin" by a header.

- **Client IP is the socket peer, not `X-Forwarded-For`.** `RemoteIP` (stamped
  into audit events and re-forwarded downstream) comes from the transport peer
  address. `X-Forwarded-For` is honored only when
  `NetHTTPOptions.TrustProxyHeaders` is set, which you do only behind a proxy
  that rewrites the header. Direct callers cannot forge their source IP.

- **Disclosure endpoints are gated on an authed gateway.** The explorer UI
  (`/rpc/_explorer/`) and the manifest (`/rpc/_manifest`) disclose the full
  catalog, plugin list, and internal mesh addresses. On a gateway that has an
  auth binding, anonymous callers get 401; set the plugin's `Public: true` to
  intentionally open them. A no-auth (local/dev) gateway serves them freely.

- **The registry refuses to boot wide open.** A registry-mode gateway whose
  `/rpc/_register` has no admission gate (mesh secret, register token, or an
  `AllowedNames` allowlist) refuses to start. Set
  `registry.Config.AllowOpenRegister: true` to boot anyway (dev/trusted
  networks) — it then boots with a loud warning rather than silently accepting
  any pod.

- **Mesh joins are HMAC-gated and replay-resistant.** With `meshsecret`
  registered, every `/rpc/_register` POST is HMAC-signed with the shared mesh
  secret; the registry rejects bad signatures, timestamps outside a ±5 min skew
  window, and exact replays inside that window (the signature doubles as a
  nonce). See [meshsecret](../gateway/builtin/meshsecret).

- **A panicking handler cannot crash the process.** Every HTTP surface — the
  rpc surface, MCP `tools/call`, the batch / batchstream per-entry goroutines,
  static, explorer, custom surfaces — dispatches through one seam that recovers
  panics, routes them through the recovery machinery, and returns a clean 500.
  A single bad request or handler bug degrades to one failed call.

## DoS limits (defaults)

| Limit | Default | Knob |
|---|---|---|
| Request body size | 4 MiB | `NetHTTPOptions.MaxBodyBytes` |
| Total read time (headers + body) | 30 s | `NetHTTPOptions.ReadTimeout` |
| Batch entries per call | 500 | `batch.Config.MaxBatchSize` / `batchstream.Config.MaxBatchSize` |
| Concurrent dispatch within a batch | 32 | `batch.Config.MaxConcurrency` / `batchstream.Config.MaxConcurrency` |
| Registrant heartbeat interval (TTL basis) | clamped to 300 s | — |

`ReadTimeout` closes the slow-body (slowloris) variant that `ReadHeaderTimeout`
alone leaves open. The batch caps bound the amplification factor of a single
`/rpc/_batch` request; the heartbeat clamp stops a hostile registrant from
requesting a near-immortal TTL.

## Consumer responsibilities

Sov deliberately does not ship these. They are edge/deployment concerns, and a
built-in would either be wrong for most deployments or duplicate infrastructure
you already run. Each has a seam.

- **Rate limiting / quota.** No limiter runs by default — rate policy is a
  deployment concern. Enforce at your L7 proxy, API gateway, or load balancer,
  or register the optional `ratelimit` builtin (token bucket, keyed by subject
  then source IP; `gw.Use(ratelimit.New(ratelimit.Config{RequestsPerSecond: 20,
  Burst: 40}))`). It runs on the `HeaderParser` seam after bearer resolution, so
  it is a quota limiter, not an auth-brute-force shield — keep that at the edge.
  Any custom policy plugs into the same seam: a `HeaderParser` or `DispatchHook`
  plugin can count and reject by the subject and client IP Sov hands it.

- **TLS termination.** Terminate at the proxy/ingress, or serve TLS in-process:
  set `NetHTTPOptions.TLSConfig` (inline certs, e.g. from a secret manager) or
  `NetHTTPOptions.CertFile`/`KeyFile` (PEM on disk); a `TLSConfig` set on a
  supplied `HTTPServer` is also honored. Any of these switches the server from
  plaintext to `ListenAndServeTLS`. Sov does not manage certificate issuance or
  rotation-on-disk. Mesh pod-to-pod defaults to plaintext `http://` — front it
  with mTLS at the transport (service mesh / sidecar) on an untrusted network.

- **CSRF + CORS.** Sov auth is **bearer-token** (`Authorization: Bearer`), which
  is CSRF-immune: a browser does not attach a bearer to a cross-site request the
  way it attaches ambient cookies, so there is no cross-site request forgery
  surface for the default posture. If you put **cookie/session auth** in front of
  sov, CSRF becomes **your** responsibility (SameSite cookies, a CSRF token, or
  an `Origin` check). The `cors` builtin defaults to allow-any-origin — safe
  under bearer auth (no credentials ride along), and convenient for the browser
  explorer; set `cors.Config.Origins` to an allowlist for a cookie-credentialed
  or hardened deployment.

- **Network isolation.** `upstreams` is a coarse network-topology filter, **not
  authentication** — `X-Sov-Upstream` is a plain client header. On an untrusted
  network, cryptographic upstream trust means `hmacseal` keyed to the mesh
  secret, not the allowlist alone.

- **Secret management.** The mesh secret, signing keys, and register tokens are
  supplied by you (env, file, secret manager). Sov holds them in memory for the
  process lifetime and never persists them.

- **The data store.** Business persistence is not in Sov's request path at all;
  routers own their storage.

## Reporting

Pre-1.0 project; report suspected vulnerabilities privately to the maintainer
rather than in a public issue.
