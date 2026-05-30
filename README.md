# Sov

**PEMM — Protocol-Enforced Modular Monolith for Go.** One module, five
packages, zero external dependencies. The gateway is the single entry
point: register methods, remote services, middleware, and roles directly
on it. Same call shape for local and remote — the resolver chain
decides. Same handler code runs as a single binary, as N pods behind a
gateway, or as a hybrid that does both at once.

> Wire: `POST /rpc/{Router}/{method}` with `{"args":...}`. `args` can be
> either an array (positional) or an object (named) — same handler
> decodes both shapes. That is the entire contract.

This README is the read-me-first. Going deeper:

- [DESIGN.md](DESIGN.md) — the model: wire shape, plugin hooks, federation, auth-split.
- [docs/PEMM.md](docs/PEMM.md) — what "Protocol-Enforced Modular Monolith" means and why it's distinct (enforcement axis, fused boundary).
- [docs/WIRE_CONTRACT.md](docs/WIRE_CONTRACT.md) — the exact, language-agnostic contract a pod implements (register, signing, seal, RPC, introspect, health).
- [examples/chirp/polyglot/](examples/chirp/polyglot/) — a non-Go (Python) pod that joins the mesh as a first-class member. Producer polyglot, proven.
- [BENCHMARKS.md](BENCHMARKS.md) — the cost of PEMM: in-process ≈ µs, remote = 1 RTT, batch coalesces N→1.

## Packages

| Package | Role |
|---|---|
| `github.com/Toyz/sov` | Top-level facade. Re-exports the 80% surface — `sov.New`, `sov.Register`, `sov.RequireSubject`, common errors. |
| `github.com/Toyz/sov/rpc` | Controller layer. Reflection-based `Engine.Register(&Router{})`, transport-agnostic dispatch, built-in `Describe()`. No HTTP. |
| `github.com/Toyz/sov/gateway` | The single entry point. Owns its engine, the resolver chain, the HTTP server, auth/authz wiring, framework endpoints. |
| `github.com/Toyz/sov/signing` | Per-request Ed25519 signing middleware (request integrity — not identity). |
| `github.com/Toyz/sov/rpctest` | Test ergonomics. Handler tests are function calls. |

## 30-second start

```go
package main

import (
    "context"
    "log"

    "github.com/Toyz/sov"
)

type EchoRouter struct{}
type SayParams struct{ Msg string `json:"msg"` }

func (r *EchoRouter) Say(_ *sov.Context, p *SayParams) (map[string]string, error) {
    if p.Msg == "" {
        return nil, sov.BadRequest("msg required")
    }
    return map[string]string{"echoed": p.Msg}, nil
}

func main() {
    gw := sov.New()
    gw.Register(&EchoRouter{})
    log.Fatal(gw.ListenAndServe(context.Background(), ":8080"))
}
```

```sh
# Named
curl -s -X POST localhost:8080/rpc/Echo/say -d '{"args":{"msg":"hi"}}'
# Positional
curl -s -X POST localhost:8080/rpc/Echo/say -d '{"args":["hi"]}'
# → {"data":{"echoed":"hi"}}
```

Dispatch picks the shape by the first non-whitespace byte of `args`:
`[` → positional, `{` → named. A `sov:` struct tag drives both paths.

## Handler contract

Router structs end in `Router`. Exported methods become wire methods.
Wire namespace = type name minus the `Router` suffix.

```go
func (r *X) M(ctx *rpc.Context)              error           // params-less, no result
func (r *X) M(ctx *rpc.Context) (T, error)                   // no params, typed result
func (r *X) M(ctx *rpc.Context, p *Params) (T, error)        // typed params + result
func (r *X) M(ctx *rpc.Context, p *Params)   error           // typed params, no result
```

`Params` and result are JSON-tagged structs. Anything else panics at
boot — the panic includes the offending signature, the accepted forms,
and a hint.

Optional `PublicMethods() []string` declares which methods are public
(authz default-allows them).

Struct tag grammar (`sov:"name,pos,flags...,key=value..."`) — see
DESIGN.md for the full grammar. Drives both wire shapes, parsed once at
`Register()` time, panics at boot on dup names, dup positions, gaps,
`required+omitempty` conflict, unknown flags, or empty kv values.

Errors are typed:

```go
return nil, sov.NotFound("widget %s", id)
return nil, sov.BadRequestCode("WORKSPACE_SLUG_IN_USE", "slug taken")
```

## Three deployment shapes — same handler source

| Path | Topology |
|---|---|
| `examples/chirp/cmd/monolith/` | All services in one binary |
| `examples/chirp/cmd/hybrid/` | Some in-process, others remote — one binary |
| `examples/chirp/cmd/mesh/` | 6 containers via docker-compose (gateway + 5 pods) |
| `examples/chirp/cmd/mesh-tiered/` | Federated master/team gateway topology |
| `examples/chirp/cmd/nested-pemm/` | Two gateways in one binary via `gw.LinkPeer` |

All shapes import the SAME `examples/chirp/handlers/` packages; only
`cmd/` wiring differs. `bash examples/chirp/walkthrough.sh` produces
byte-equivalent output against all of them — that's the PEMM proof.

## Gateway = single entry point

```go
gw := sov.New(
    sov.WithAdvertiseURL("http://this-pod:8080"), // stamp X-Sov-Upstream
    sov.WithHMACSecret(secret),                   // seal X-Sov-* bundle
)

// Local in-process — engine.Register sugar.
gw.Register(&UserRouter{...})

// Remote — equivalent of the pod POSTing _register at startup.
gw.RegisterRemote("Widgets", "http://widgets-pod:9001", 30*time.Second)

// Auth + authz: detected by interface in monolith mode.
gw.Register(&AuthRouter{...})    // sov.AuthService → auto-bound
gw.Register(&PolicyRouter{...})  // sov.AuthzService → auto-bound

// One entry point for plugins + middleware.
gw.Use(slog.Default())                            // *slog.Logger satisfies gateway.Logger
gw.UseAll(preset.Monolith(preset.MonolithConfig{})...)
gw.Use(loggingMiddleware)

log.Fatal(gw.ListenAndServe(ctx, ":8080"))
```

The three-domain auth split (Identity / Authorization / Profile) is
enforced by the framework — `Claims` has no `Role` field. See
[DESIGN.md → Three-domain auth split](DESIGN.md#three-domain-auth-split).

## Mesh pod

```go
gw.JoinMesh(ctx, sov.MeshOptions{
    UpstreamURL: "http://gateway:8080",
    Address:     ":9001",
    Advertise:   "http://auth:9001",
    Roles:       sov.RoleAuth,           // self-declare role (RoleAuth | RoleAuthz)
    MeshSecret:  []byte(os.Getenv("SOV_MESH_SECRET")),
})
```

Mesh hardening (signed `_register`, role-conflict guard, name allowlist)
is built into the `registry`, `meshsecret`, `roletakeover`, and
`preempt` plugins — see [DESIGN.md → Federation + tiered mesh](DESIGN.md#federation--tiered-mesh).

## Cross-service calls — `sov.Client`

```go
// Monolith: gw.LocalClient() — no HTTP loopback, dispatches in-process.
// Mesh:     sov.NewClient(gwURL) — HTTPs through the central gateway.
var u User
err := cli.Call(ctx, "User", "get", &GetParams{ID: "u_1"}, &u)
```

Auto-forwards the inbound `Authorization` header so identity propagates
across hops.

## Pluggable Server

The HTTP server is `gateway.Server` — an interface. Default
`gateway.NewNetHTTPServer(opts)` uses `net/http`. Plug in fiber /
fasthttp / echo by implementing two methods (`Handle`,
`ListenAndServe`) and passing via `sov.WithServer(...)`. Sov ships no
fiber adapter — that is your choice, not the framework's.

`NetHTTPOptions.TrustUpstreamClaims` (or `sov.WithTrustUpstreamClaims(true)`)
— pods set this to accept `X-Sov-*` from a trusted upstream gateway.
Edge gateways leave the default (strip).

## Plugins — sov-itself-PEMM

Sov's extension model IS PEMM applied to the framework. A plugin is a
Go type satisfying one or more interface hooks; `gw.Use(plugin)` auto-
detects each. A plugin can ALSO be a sov service.

```go
gw.Use(audit.New(audit.Config{Out: os.Stdout}))   // DispatchHook + IntrospectContributor + Audit.recent RPC
gw.Use(slog.Default())                            // Logger
```

14 builtin plugins ship under `gateway/builtin/`. Each has its own
README with the full Config struct, hooks, and capabilities.

| Plugin | One-liner |
|---|---|
| [`audit`](gateway/builtin/audit/README.md) | DispatchHook + ring buffer + `Audit.recent` RPC |
| [`auth`](gateway/builtin/auth/README.md) | Translates Claims to legacy `X-Forwarded-*` headers |
| [`batch`](gateway/builtin/batch/README.md) | Owns `/rpc/_batch`; cascades nested batches per pod |
| [`cors`](gateway/builtin/cors/README.md) | Preflight short-circuit + per-response CORS headers |
| [`explorer`](gateway/builtin/explorer/README.md) | Embedded HTML browser + drift radar at `/rpc/_explorer/` |
| [`hmacseal`](gateway/builtin/hmacseal/README.md) | HMAC-seals `X-Sov-*` on outbound; verifies on inbound |
| [`manifest`](gateway/builtin/manifest/README.md) | Emits the PEMM manifest at `/rpc/_manifest` |
| [`meshsecret`](gateway/builtin/meshsecret/README.md) | HMAC gate on `/rpc/_register` |
| [`metrics`](gateway/builtin/metrics/README.md) | Prom-format aggregation across registered services |
| [`preempt`](gateway/builtin/preempt/README.md) | Federation takeover map; one-way, consumed on success |
| [`registry`](gateway/builtin/registry/README.md) | Promotes to registry shape — owns `_register`, `/health`, aggregation |
| [`requestid`](gateway/builtin/requestid/README.md) | Generates + propagates `X-Sov-Request-Id` |
| [`roletakeover`](gateway/builtin/roletakeover/README.md) | Drops the default `409 ROLE_CONFLICT` (blue-green) |
| [`upstreams`](gateway/builtin/upstreams/README.md) | URL allowlist of upstream gateways whose `X-Sov-*` are trusted |

Full hook interface list (20 sub-interfaces, all detected by Go type
assertion) is documented in `gateway/plugin.go`. See
[DESIGN.md → Plugin model](DESIGN.md#plugin-model--framework-as-pemm)
for the rationalized list and what each one fires on.

### Presets

`gateway/preset` bundles plugins for the common deployment shapes. Each
takes a Config struct composing per-plugin Configs.

```go
gw.UseAll(preset.Monolith(preset.MonolithConfig{Audit: audit.Config{Out: os.Stdout}})...)
gw.UseAll(preset.Pod(preset.PodConfig{HMACSeal: hmacseal.Config{Secret: secret}})...)
gw.UseAll(preset.Registry(preset.RegistryConfig{Registry: registry.Config{AllowedNames: []string{"Auth", "Authz"}}})...)
gw.UseAll(preset.Hybrid(preset.HybridConfig{})...)
```

Empty-valued config entries skip their plugin so a minimal
`preset.Registry(preset.RegistryConfig{})` call still works.

## Cascading batch + explorer

One POST to `/rpc/_batch` with N calls fans out concurrently; the
gateway groups by destination and any group with >=2 remote entries
pointing at the same pod collapses into ONE nested `/rpc/_batch` to
that pod. `sov.WithExplorer()` mounts an embedded HTML browser at
`/rpc/_explorer/` (pure reader of `/rpc/_introspect` — flat type
catalog, drift radar, live execution). Details in
[DESIGN.md → Cascading batch](DESIGN.md#cascading-batch--rpc_batch).

## CLI: `sov`

One binary, one install:

```sh
go install github.com/Toyz/sov/cmd/sov@latest
```

| Subcommand | Purpose |
|---|---|
| `sov init <mode>` | Scaffold a project (`monolith` / `hybrid` / `mesh` / …) |
| `sov gen <lang>` | Generate a typed client (`ts` / `go` / `kotlin` / `swift` / `python`) |
| `sov call` | Invoke a method (`Service.method`) and print the response |
| `sov conform` | Validate a pod against the wire contract (polyglot conformance) |
| `sov drift` | Check the gateway catalog for type-shape drift across services |
| `sov inspect` | Pretty-print `/rpc/_introspect` (services, types, ownership, plugins) |
| `sov health` | Pretty-print `/rpc/_health` |
| `sov version` | CLI version + build info |

Codegen pulls from a live gateway:

```sh
sov gen ts --from http://localhost:8080 --out ./client.ts
# Or one-shot — spawn the gateway, fetch, kill. Binary must honor SOV_LISTEN.
sov gen ts --exec /tmp/sov-monolith --out ./client.ts
```

Each language target emits a single file with typed methods + the
`batch()` helper. Drift detection prints stderr warnings naming each
variant + which services hold it; CI greps stderr if it wants to fail.

## Testing

```go
func TestWidgetCreate(t *testing.T) {
    eng := rpc.NewEngine()
    eng.Register(&WidgetRouter{Store: fakeStore})

    ctx := rpctest.New().WithUser("u_alice").Ctx()
    var w Widget
    status, err := rpctest.CallInto(eng, ctx, "Widget", "create",
        &CreateParams{Name: "foo"}, &w)
    require.Equal(t, 200, status)
}
```

Fuzz: `go test -fuzz=FuzzDispatch -fuzztime=60s ./rpc`.

## Status

Pre-1.0. Breaking changes allowed per wave. Zero external dependencies
— `go.mod` is just `module github.com/Toyz/sov` + `go 1.25.0`, pure
stdlib top to bottom.

Deferred to v0.2+: hot-path decoder closures, DNS/Istio resolvers, SSE
/ streaming, mTLS on `_register`. See [DESIGN.md → What PEMM explicitly
does NOT do](DESIGN.md#what-pemm-explicitly-does-not-do).
