# PEMM — Protocol-Enforced Modular Monolith

Sov's design document. The canonical reference for what the framework
is, what it commits to, and what it deliberately refuses. README.md is
the "start here" doc; this file is the "now I want to understand the
model" doc.

---

## What PEMM is

**One handler source — three deployment shapes.** Sov is the modular
monolith done correctly: the wire format (not directory layout, not a
sidecar, not a service mesh) is the boundary between services. Because
the boundary is the protocol, the same Go handler package compiles into
a single-binary monolith, a hybrid (some services in-process, others
remote), or a fully-deployed mesh of N pods — without changing a line
of handler code.

This is the load-bearing claim: **the binary topology is a config
decision, not an architecture decision.** Every other framework
commits to a topology at design time. gRPC, Connect, Twirp all assume
cross-process from method-one. Modular monoliths assume in-process
forever. PEMM defers the choice to deploy time and lets a single
gateway host both shapes simultaneously.

---

## The wire shape

Sov has exactly one wire contract:

```
POST /rpc/{Router}/{method}      Content-Type: application/json
Authorization: Bearer <jwt>      (optional)

Body:        { "args": ... }
Resp 200:    { "data": <result> }
Resp >= 400: { "error": { "message": "...", "code": "UPPERCASE_SNAKE" } }
```

`args` is dual-shape: `[...]` (positional) or `{...}` (named). The
dispatcher picks the shape by the first non-whitespace byte. A `sov:`
struct tag (`sov:"name,pos,omitempty,required,deprecated,title=...,desc=...,doc=...,example=..."`)
drives both paths from the same Go struct.

### Framework endpoints

All framework concerns live under `/rpc/_*`:

| Endpoint | Default | Plugin | Purpose |
|---|---|---|---|
| `POST /rpc/{Router}/{method}` | always | engine | business dispatch |
| `POST /rpc/_batch` | always | `batch` | cascading fan-out |
| `GET  /rpc/_health` | always | engine | internal probe |
| `GET  /rpc/_introspect` | always | engine + plugins | type catalog + service descriptors |
| `POST /rpc/_register` | OFF | `registry` | mesh control plane |
| `GET  /health` | OFF | `registry` | k8s-friendly public probe |
| `GET  /rpc/_manifest` | OFF | `manifest` | PEMM manifest snapshot |
| `GET  /rpc/_explorer/` | OFF | `explorer` | embedded HTML browser |

Service-level `/rpc/{Router}/_X` endpoints are refused at the gateway
by design — only reachable via direct intra-cluster addressing.

---

## Registering methods: reflective vs typed

`gw.Register(&Router{})` reflects the struct's methods at boot — ergonomic,
zero boilerplate, and the reflection cost lands on dispatch (`reflect.Value.Call`
per request). At realistic scale that cost is noise (local dispatch ≈ µs,
dominated by JSON, not the call), but for hot methods you can opt into a
typed registration that builds the dispatch closure at boot and calls the
handler directly — no `reflect.Call`/`reflect.New` in the hot path:

```go
rpc.Handle(eng, "Chirp", "post",
    func(ctx *rpc.Context, p *chirps.PostParams) (*chirps.Chirp, error) { ... })
rpc.HandleErr(eng, "Chirp", "delete",
    func(ctx *rpc.Context, p *chirps.DeleteParams) error { ... })
```

The bigger win is **compile-time-checked handler signatures** — a wrong
shape is a build error, not a boot panic. Field decoding still uses the
boot-built field map (so both arg shapes + `sov` tags work identically).
`Handle` and `Register` coexist on one engine. (Reflection-free *field
decode* too would need codegen — a `sov gen dispatch` Tier-3, deferred.)

## Three deployment shapes from one source

The gateway is the only entry point. Local services and remote services
flow through the same engine, the same auth chain, the same dispatch
goroutine.

### Monolith

```go
gw := sov.New()
gw.UseAll(preset.Monolith(preset.MonolithConfig{})...)
gw.Register(&AuthRouter{})
gw.Register(&UserRouter{})
gw.Register(&ChirpRouter{})
log.Fatal(gw.ListenAndServe(ctx, ":8080"))
```

One binary, every service in-process. Cross-service calls via
`gw.LocalClient()` go through the engine — no HTTP loopback. See
`examples/chirp/cmd/monolith/`.

### Hybrid

```go
gw.Register(&AuthRouter{})                                        // in-process
gw.RegisterRemote("Chirp", "http://chirp-pod:9001", time.Minute)  // remote
```

The same binary hosts some services and proxies others. The resolver
chain picks per request. See `examples/chirp/cmd/hybrid/`.

### Mesh

Pods self-register against a central registry gateway via `JoinMesh`:

```go
gw.JoinMesh(ctx, sov.MeshOptions{
    UpstreamURL: "http://registry:8080",
    Address:     ":9001",
    Advertise:   "http://chirp-pod:9001",
})
```

A registry gateway runs `preset.Registry(...)`; pods run `preset.Pod(...)`.
The `walkthrough.sh` script in `examples/chirp/` produces byte-equivalent
output against monolith, hybrid, and mesh — that's the PEMM proof. See
`examples/chirp/cmd/mesh/`.

**Scaling the registry across replicas.** The registry's service→address
map lives behind the `RegisterStore` interface (default: in-memory, per
replica). Run several registry replicas behind a load balancer and each
only sees the pods whose heartbeat landed on it — partial-view drift. Fix
it with a shared store + periodic refresh:

```go
rr := gateway.NewRegisterResolver(5*time.Second,
    gateway.WithRegisterStore(myRedisStore),   // shared source of truth
    gateway.WithRegisterRefresh(10*time.Second)) // pull peers' registrations
gw := sov.NewRegistry(cfg, sov.WithRegisterResolver(rr))
```

Writes (heartbeats) go to the shared store; every replica refreshes its
lock-free local read cache on the interval, so `Resolve` stays in-memory
fast (never a per-request round trip) while all replicas converge to one
mesh view. The Redis impl is yours (keeps the zero-dependency core clean);
the seam + the local-cache/refresh machinery are built in.

### Tiered mesh (federation)

Master gateway fronts N team gateways, each fronting its own pods. Team
gateways announce their services via `JoinMesh`. Prefer `FederateAll: true`,
which advertises the team gateway's LIVE resolver set (sub-pods + local
routers) and recomputes it on every heartbeat — so a sub-pod that joins is
federated next beat and one that leaves TTL-expires, keeping the master's
introspect + health in sync with zero hand-maintained lists. `Federate:
[]string{...}` remains for an explicit fixed list. Same wire shape
(`_register`), one extra flag. See `examples/chirp/cmd/mesh-tiered/`.

**Per-tier join secrets.** A team gateway is both a registry (its sub-pods
register to it) and a pod (it registers upstream), so it holds two
independent join secrets: `RegistryConfig.MeshSecret`/`.RegisterToken`
gates *its own* `/rpc/_register` (the down secret its sub-pods present);
`MeshOptions.MeshSecret`/`.RegisterToken` is what it presents *upstream*
(the up secret). Set them to different values per tier and a leaked team
secret can register a sub-pod in that team only — not join prime or
impersonate another team. The mesh-tiered example reads
`SOV_UPSTREAM_MESH_SECRET` (defaulting to `SOV_MESH_SECRET`) to model this.

### Nested PEMM

Two gateways in one binary, linked via `gw.LinkPeer(peer, "Admin")`.
Useful for in-process admin/public splits without an HTTP hop. See
`examples/chirp/cmd/nested-pemm/`.

---

## Plugin model — framework-as-PEMM

Sov's extension model IS PEMM applied to the framework. A plugin is a
Go type that satisfies one or more interface hooks; the gateway
auto-detects each via Go interface assertion in `gw.Use(plugin)`. A
plugin can ALSO be a sov service — if the type's name ends in `Router`,
the same `Use` call registers its methods on the engine.

The 14 builtin plugins ship under `gateway/builtin/` and each satisfies
the same interfaces a consumer plugin would write. No annotations, no
DI, no codegen.

### Hook interfaces

Defined in `gateway/plugin.go`. Implement only what you need.

**HeaderHooks cluster** (`gateway/plugin.go:54-93`)
- `HeaderInjector.InjectHeaders` — add headers on outbound proxy hops
- `HeaderParser.ParseHeaders` — read inbound non-sov headers
- `HeaderClaimer.ClaimedHeaders` — declare header names that bypass the framework's identity-strip

**TrustHooks cluster** (`gateway/plugin.go:248-271`)
- `UpstreamTrustPolicy.TrustUpstream` — vet by upstream URL allowlist
- `SealVerifier.VerifySeal` — vet by HMAC seal

**Identity / dispatch**
- `AuthTranslator` — translate verified Claims into legacy headers (`X-Forwarded-User`, etc.)
- `DispatchHook.OnDispatch` — fires after every handler returns; metrics, audit, tracing
- `Middlewarer.Wrap` — chi-style middleware as a plugin
- `ContextContributor.ContributeContext` — stash per-request data onto the local `*rpc.Context`

**Lifecycle**
- `BootValidator.ValidateBoot` — refuse startup with a clear error
- `LifecycleHook.OnStart/OnStop` — background goroutines, pools
- `ConfigApplier.Apply` — mutate the gateway at registration time, BEFORE other hooks fire

**Aggregation / observability**
- `IntrospectContributor.ContributeIntrospect` — decorate `/rpc/_introspect` OR fan out to remotes (replaces the prior `IntrospectAugmenter` + `IntrospectAggregator` pair)
- `HealthAggregator.AggregateHealth` — merge remote-pod health probes into `/rpc/_health`
- `RouteHandler.RoutePatterns/ServeRoute` — own a path on the gateway without wrapping the dispatch chain
- `ResponseInterceptor.InterceptResponse` — mutate any outgoing `*Response`

**Mesh policy**
- `MeshConflictPolicy.AllowMeshConflict/ConsumeConflict` — decides role-takeover AND federation-preemption conflicts via the `Conflict` discriminator (`Role: RoleFlag` for role takeover, `FederatedAddrs: [2]string` for federation). Replaces the prior `RoleConflictPolicy` + `FederationPreemptionPolicy` pair.

**Cross-plugin coordination**
- `PluginDependency.Requires/After` — hard deps (fail-fast) + soft ordering (advisory)
- `CapabilityProvider.Capabilities` — publish typed contracts under `"<plugin>.<contract>"` namespace
- `Logger.Debug/Info/Warn/Error` — slog-compatible sink; `*slog.Logger` satisfies it directly
- `Plugin.PluginName` — optional diagnostic marker

### One entry point

```go
gw.Use(audit.New(audit.Config{Out: os.Stdout}))
gw.Use(slog.Default())            // satisfies Logger directly
gw.UseAll(preset.Monolith(cfg)...)
```

### The 14 builtin plugins

Every framework concern is also a plugin. Same interface contracts a
consumer plugin would write.

| Plugin | One-liner |
|---|---|
| `audit` | DispatchHook + ring buffer + `Audit.recent` RPC |
| `auth` | Translates verified Claims to legacy `X-Forwarded-*` headers |
| `batch` | Owns `/rpc/_batch`; cascades nested batches per pod |
| `cors` | Preflight short-circuit + per-response CORS headers |
| `explorer` | Embedded HTML browser + drift radar at `/rpc/_explorer/` |
| `hmacseal` | HMAC-seals `X-Sov-*` headers on outbound; verifies on inbound |
| `manifest` | Emits the PEMM manifest at `/rpc/_manifest` |
| `meshsecret` | HMAC join gate on `/rpc/_register` (signed body + timestamp) |
| `registertoken` | Simple shared-token join gate on `/rpc/_register` (kubeadm-style bearer) |
| `metrics` | Prom-format aggregation across registered services |
| `preempt` | Per-service federation takeover map; one-way, consumed on success |
| `registry` | Promotes to registry shape — owns `_register`, `/health`, aggregation |
| `requestid` | Generates + propagates `X-Sov-Request-Id` across all paths |
| `roletakeover` | Drops the default `409 ROLE_CONFLICT` guard (blue-green) |
| `upstreams` | URL allowlist of upstream gateways whose `X-Sov-*` are trusted |

Per-plugin Config struct + hook set in each plugin's `README.md`.

### Replacements for plugins removed in cleanup

| Removed plugin | Now |
|---|---|
| `slogger` | `gw.Use(slog.Default())` — `*slog.Logger` satisfies `gateway.Logger` |
| `advertise` | `sov.WithAdvertiseURL(url)` option |
| `allowlist` | `registry.Config{AllowedNames: []string{...}}` field |
| `localpeer` | `gw.LinkPeer(peer, services...)` method |
| `drift` | `sov drift` CLI subcommand (was server-side polling) |

---

## Three-domain auth split

The framework enforces a clean separation:

| Concept | Owner | On the wire |
|---|---|---|
| **Identity** (subject id) | `AuthService` | `X-Sov-Subject`, `X-Sov-Issuer`, `X-Sov-Expires`, `X-Sov-Seal` |
| **Delegated power** (scopes) | `AuthService` | `X-Sov-Scopes` |
| **Authorization** (allow/deny) | `AuthzService` | Decision-time only; **never on the wire** |
| **Profile** (display, handle, ...) | `UserService` (your code) | Looked up by callers when needed |

`Claims` is identity + delegation only — no `Role` field. Stuffing
roles into the token turns ambient identity into ambient policy and
makes hot-reload impossible. RBAC is the authz service's job.

The gateway:

1. Strips inbound `X-Sov-*` (anti-smuggling).
2. Calls `AuthService.Verify(token)` once per fresh bearer, caching the
   result until `ExpiresAt`. The cache is the `ClaimsCache` interface —
   default in-memory per-replica; pass `WithClaimsCache` a shared (Redis,
   etc.) impl so a fleet of gateway replicas reuse each other's verify
   results. The gateway re-checks `ExpiresAt` after a cache hit, so a stale
   entry is never honored.
3. Calls `AuthzService.Check(claims, service, method)` on EVERY request — anonymous included.
   - `{Allow: true}` → proceeds.
   - `{Allow: false, Authenticate: true}` → 401.
   - `{Allow: false, Reason: ...}` → 403.
4. Injects `X-Sov-Subject/Issuer/Scopes/Expires` downstream when proxying
   (plus `X-Sov-Seal` when the optional `hmacseal` plugin is enabled).

In monolith mode `gw.Register(&AuthRouter{})` auto-detects the
`sov.AuthService` interface and binds it. Explicit forms
(`gw.RegisterAuth`, `gw.RegisterAuthz`) work too.

Inter-service identity is **trust-by-default**: a pod that opts in with
`WithTrustUpstreamClaims(true)` accepts the `X-Sov-*` bundle from its
gateway with no per-request crypto — so monolith, hybrid, and mesh behave
identically with zero seal wiring, and relocating a service is free. The
HMAC seal (via `hmacseal`, keyed to the mesh secret) is **opt-in
hardening** for when the gateway↔pod network isn't trusted; without it a
trusting pod relies on network isolation, and a non-trusting pod (the
default, e.g. an edge gateway) strips `X-Sov-*` so clients can't smuggle
identity.

---

## Cross-plugin coordination

Plugins compose without DI:

- **Lookup**: `gw.PluginByName("audit")` returns the registered plugin value.
- **Capabilities**: typed contracts under `"<plugin>.<contract>"` namespace. Producer publishes via `CapabilityProvider`; consumer reads via `gateway.GetCapability[T](gw, "audit.Recent")`. First publisher wins; framework records all for drift detection.
- **Dependencies**: `PluginDependency.Requires()` fails fast if a dep isn't registered; `After()` is an advisory ordering hint surfaced in introspect.
- **Header claims**: `HeaderClaimer.ClaimedHeaders()` declares which inbound HTTP header names bypass the framework's `X-Sov-*` identity-strip. Used today by `meshsecret` (`X-Sov-Register-Sig`, `X-Sov-Register-Ts`) and `requestid` (`X-Sov-Request-Id`).

---

## Federation + tiered mesh

The `_register` payload is a small JSON envelope (`name`, `address`,
`federate?: []string`, `roles?: RoleFlag`, optional MAC under the
`meshsecret` plugin). A team gateway announces a bundle of services as one
route — `FederateAll: true` (advertise the live set, recomputed each
heartbeat) or `Federate: []string{...}` (explicit fixed list).

### IntrospectContributor cascade

`IntrospectContributor.ContributeIntrospect(ctx, report, trace, visited)`
lets a plugin fan out to remote pods + merge their descriptors. The
`visited` slice carries the normalized address list of gateways already
visited on this cascade; plugins MUST append themselves before fanning
out and skip any address already in `visited`. Cycles short-circuit with
a `cycle-skipped` annotation in the merged report.

### MeshConflictPolicy

`AllowMeshConflict(current, candidate, c Conflict) bool` decides whether
an inbound `_register` may take over an existing claim:

- **Role takeover** (`c.Role != 0`): a name already bound to an
  auth/authz role wants to be rebound. The framework iterates registered
  policies; first true wins. Default is deny → `409 ROLE_CONFLICT`. The
  `roletakeover` plugin opts back into last-write-wins.
- **Federation preemption** (`c.FederatedAddrs[0] != ""`): same wire
  name claimed by a different address. The `preempt` plugin holds the
  map; one-way, exact-address, consumed on success via `ConsumeConflict`.

### AllowedNames

`registry.Config{AllowedNames: []string{...}}` restricts `/rpc/_register`
to a fixed set of wire service names. A leaked mesh-secret can't be used
to claim an unexpected role-bound name (attacker has the key but isn't
supposed to be `Auth`).

---

## Cascading batch — `/rpc/_batch`

One POST with N method calls fans out concurrently. The gateway resolves
each entry, **groups by destination**, and any group with >=2 remote
entries pointing at the same pod POSTs ONE nested `/rpc/_batch` to that
pod (which runs its own concurrent fan-out). Saves the (N-1) extra HTTP
round trips a per-entry path would burn.

Defaults (zero-config):

- Always coalesce >=2 remote entries to the same service.
- Auto-fallback on 404 (downstream pod doesn't implement `_batch`); the
  404 is cached for 60s so non-batch-aware pods don't cost a wasted RTT
  every batch.
- No recursion guard — PEMM forbids same-batch cross-references between
  services, so the loop concern doesn't apply.
- Auth + `X-Sov-*` claim headers propagate to the rebatched POST.
- Full-pod failures land every alias in the group with an
  `UPSTREAM_UNAVAILABLE` envelope.

See `examples/chirp/walkthrough.sh` for a working sample and
`gateway/builtin/batch/` for the implementation.

---

## Codegen + the introspect catalog

`/rpc/_introspect` is the framework's source of truth: flat type
catalog (every Go type appearing on any service's request, with
`used_by` references), service descriptors, plugin list (with satisfied
hooks + capabilities + per-plugin `extra`), and `cross_refs` (drift
radar — any type name with multiple ShapeHash variants).

The catalog is cached 30s and push-invalidated when pods register or
expire.

### `sov gen <lang>` — five language targets

Single `sov` binary, five language outputs, each emitted as a single
file:

```
sov gen ts     --from http://localhost:8080 --out ./client.ts
sov gen go     --from http://localhost:8080 --out ./client.go
sov gen kotlin --from http://localhost:8080 --out ./Client.kt
sov gen swift  --from http://localhost:8080 --out ./Client.swift
sov gen python --from http://localhost:8080 --out ./client.py
```

`--exec <binary>` spawns the binary on a free port, fetches introspect,
kills it. Useful for CI — `sov gen ts --exec ./bin/monolith --out client.ts`.

Drift detection: when `cross_refs` is non-empty, the CLI prints a stderr
warning naming each variant + which services hold it, then
deterministically picks the first-alphabetical variant. Exit code stays
0 — CI greps stderr if it wants to fail.

`sov drift --from <url>` is the CI-friendly standalone check (was the
server-side `drift` plugin — moved to the operator side, since carrying
a polling detector in the gateway is unnecessary when CI can run it
against every PR).

---

## CLI: `sov`

One binary, installed via `go install github.com/Toyz/sov/cmd/sov@latest`:

| Subcommand | Purpose |
|---|---|
| `sov gen <lang>` | Generate a typed client (ts, go, kotlin, swift, python) |
| `sov drift` | Check the gateway catalog for type-shape drift across services |
| `sov inspect` | Pretty-print `/rpc/_introspect` (services, types, plugins) |
| `sov health` | Pretty-print `/rpc/_health` (rollup, per-service) |
| `sov version` | Print sov CLI version + build info |
| `sov help` | Top-level help |

Stdlib `flag` only — no Cobra dep, static binary, zero transitive imports.

---

## What PEMM explicitly does NOT do

- **Streaming / SSE** — out for v1. Wire complexity, ergonomic regressions for the dual-shape ingress.
- **A built-in tenant service** — opinionated, consumer-owned.
- **mTLS on `_register`** — the HMAC seal (`meshsecret` + `hmacseal`) covers most threats; transport-layer defense in depth is deferred.
- **protobuf-strict types** — the introspect catalog is Go-shape JSON; consumers generate their language target from it.
- **Non-Go server side** — clients in 5 languages via codegen, but server-side handlers are Go.

---

## Status

Pre-1.0. Breaking changes allowed per wave.

**Zero external dependencies.** `go.mod` is:

```
module github.com/Toyz/sov
go 1.25.0
```

Pure stdlib. Every plugin, every example, every codegen target.
