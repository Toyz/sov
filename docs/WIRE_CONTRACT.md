# Sov Wire Contract

This is the complete over-the-wire contract a process must satisfy to act as a **sov pod** —
register into a mesh, survive heartbeats, serve RPC, and answer introspect/health. It is
**language-agnostic**: nothing here requires Go or the `sov` framework. A pod written in any
language that speaks this contract is a first-class mesh member (producer polyglot).

Every section cites the Go source that defines it so this document cannot silently drift from
the implementation. When in doubt, the cited code wins.

A working reference implementation in pure-stdlib Python lives at
[`examples/chirp/polyglot/chirp_pod.py`](../examples/chirp/polyglot/chirp_pod.py). Validate any
pod against this contract with `sov conform` (see [§8](#8-conformance)).

---

## 0. Transport basics

- Transport is HTTP/1.1. Bodies are JSON (`Content-Type: application/json`).
- Every RPC path is `POST /rpc/{router}/{method}`. Framework endpoints (`_health`, `_introspect`,
  `_batch`, `_register`) live under `/rpc/_*` and accept `GET` or `POST` except `_register`/`_batch`
  which are `POST`. `_introspect` is **opt-in** (off by default → 404; see [§5](#5-rpc_introspect-and-rpc_health)).
- Router and method names are case-sensitive. Router names beginning with `_` are reserved.

---

## 1. RPC request / response envelope

Source: `rpc/wire.go`, `rpc/dispatch.go`, `rpc/errors.go`.

### Request

```json
{ "args": <positional-array | named-object> }
```

`args` accepts **two interchangeable shapes** — the server picks the path by inspecting the first
non-whitespace byte (`[` vs `{`):

- **Positional:** `{"args":[v0, v1, v2]}` — bound by `sov` tag position (or source order).
- **Named:** `{"args":{"field":v, ...}}` — bound by `sov` tag name (or `json` tag, or
  `snake_case(GoFieldName)`).
- **Single-object-in-array** `{"args":[{...}]}` is treated as **named** (the lone object is the
  params object). This is the backward-compat shape clients commonly emit.
- No-args methods accept `{}`, `{"args":null}`, or `{"args":[]}`.

Unknown named keys are ignored. Missing required (non-`omitempty`) fields → `400 BAD_REQUEST`.

A pod MUST accept **both** shapes for every method. (`sov conform` round-trips both.)

### Success response — HTTP 200

```json
{ "data": <method-return-value> }
```

`data` is the JSON-marshaled return value, or `null` when the method returns no value.

### Error response — HTTP ≥ 400

```json
{ "error": { "message": "...", "code": "UPPER_SNAKE", "error_code": "optional-stable-code" } }
```

`code` is the coarse category; `error_code` is an optional stable app-level code.

### Status codes

Source: `rpc/errors.go`.

| Code | Meaning |
|---|---|
| 200 | success |
| 400 | `BAD_REQUEST` — malformed input |
| 401 | `UNAUTHORIZED` — auth required / invalid token |
| 403 | `FORBIDDEN` — authenticated but not allowed |
| 404 | `NOT_FOUND` — router/method unknown |
| 409 | `CONFLICT` — state conflict (role/service takeover on `_register`) |
| 429 | `TOO_MANY_REQUESTS` |
| 500 | `INTERNAL` |
| 503 | `UNAVAILABLE` |

---

## 2. `/rpc/_register` — joining the mesh

Source: `gateway/framework.go` (`RegisterRequest`/`RegisterResponse`),
`gateway/builtin/registry/registry.go` (handler).

`POST /rpc/_register` with body:

```json
{
  "name": "Chirp",
  "address": "http://chirp-pod:9002",
  "heartbeat_interval_seconds": 5,
  "auth": false,        "verify": "verify",
  "authz": false,       "check": "check",
  "introspect": true,
  "federate": false,    "services": []
}
```

| Field | Required | Meaning |
|---|---|---|
| `name` | yes | wire name of the service this pod exposes. Cannot start with `_`. Ignored when `federate=true`. |
| `address` | yes | URL the gateway uses to reach this pod. Normalized (trailing `/` stripped). HTTP/HTTPS only. |
| `heartbeat_interval_seconds` | yes | re-register cadence. TTL = interval × 3. |
| `auth` / `verify` | no | declare this pod the auth verifier; `verify` is the method name (default `verify`). See [§6](#6-auth--authz-roles). |
| `authz` / `check` | no | declare this pod the authz policy hook; `check` is the method name (default `check`). |
| `introspect` | no | opt into `/rpc/_introspect` aggregation. The gateway may force this true (see response). |
| `federate` / `services` | no | this pod is a tiered gateway fronting many routers; `services` lists every router name; `name` becomes a label. Federated pods cannot hold auth/authz roles. **Send your LIVE service set on every heartbeat** so the upstream stays in sync — a name that appears is federated next beat, one that drops out TTL-expires. (Go: `MeshOptions.FederateAll` recomputes it for you; otherwise rebuild `services` yourself before each register.) |

Success — HTTP 200:

```json
{ "data": { "ok": true, "ttl_seconds": 15, "force_introspect": false } }
```

`force_introspect: true` means the gateway has a catalog consumer (e.g. the explorer) and flipped
your entry to introspectable regardless of what you requested — the pod SHOULD set `introspect:true`
on subsequent heartbeats to stay consistent.

Failure codes: `400 BAD_REQUEST`, `401` (bad/missing mesh-secret signature, see [§3](#3-join-gates-control-plane)),
`403 FORBIDDEN` (name not on the registry allow-list), `409 ROLE_CONFLICT` / `409 SERVICE_CONFLICT`
(another pod holds the role/name; a takeover policy plugin may permit override).

### Heartbeat

Source: `gateway/mesh.go`.

Re-POST the identical `/rpc/_register` body every `heartbeat_interval_seconds`. TTL = interval × 3,
so missing one beat is survivable; missing three expires the entry silently (no goodbye message).
Stop on shutdown — the entry times out.

---

## 3. Join gates (control plane)

`/rpc/_register` is open by default (any reachable process can join). Two opt-in gates control **who
may join** — independent, composable, and distinct from `registry.AllowedNames` (which gates *which
names*) and the `X-Sov-*` data plane (per-request identity):

| Gate | Pod sends | Strength | Use |
|---|---|---|---|
| **register token** (`builtin/registertoken`) | `X-Sov-Register-Token: <token>` (static) | bearer — replayable, rotate it | kubeadm/Consul-style bootstrap join; one static header, no crypto |
| **mesh-secret** (`builtin/meshsecret`) | HMAC sig over body+ts (below) | body-bound + ±5min replay window | hardened join proof |

Enable either on the gateway (`RegistryConfig.RegisterToken` / `.MeshSecret`) and supply the matching
value on the pod (`MeshOptions.RegisterToken` / `.MeshSecret`). A missing/wrong value gets `401`. With
both enabled, a pod must satisfy both.

### Mesh-secret signing

Source: `gateway/builtin/meshsecret/proto/proto.go`.

When the gateway runs the `meshsecret` plugin, **every** `/rpc/_register` POST must carry two headers:

- `X-Sov-Register-Ts`: Unix timestamp in **seconds**, ASCII decimal.
- `X-Sov-Register-Sig`: lowercase hex HMAC-SHA256 over the **canonical message**.

Canonical message (newline-terminated, exactly):

```
v1\n
register\n
<sha256_hex(request_body_bytes)>\n
<unix_ts_seconds>\n
```

i.e. the literal string `"v1\nregister\n" + hex(sha256(body)) + "\n" + str(ts) + "\n"`.

```
sig = hex( HMAC_SHA256( mesh_secret, canonical_message ) )
```

The gateway recomputes and constant-time compares; it rejects any request whose `X-Sov-Register-Ts`
is more than **±5 minutes** (`SkewWindow`) from its own clock. The secret is shared out-of-band; it
is never transmitted. The same mesh secret can also key the optional inter-service seal ([§4](#4-identity-propagation--the-optional-x-sov-seal)) — one secret for both.

> Recompute the SHA-256 over the **exact bytes you send** as the body — serialize once, hash those
> bytes, then send those same bytes.

---

## 4. Identity propagation + the optional `X-Sov-Seal`

Source: `gateway/builtin/hmacseal/proto/proto.go`, `gateway/seal.go`, `gateway/nethttp.go`.

The gateway forwards a verified caller identity to downstream pods as `X-Sov-*` headers:

- `X-Sov-Subject` — caller id (opaque)
- `X-Sov-Issuer` — which auth service minted it (optional)
- `X-Sov-Scopes` — comma-joined scopes (optional)
- `X-Sov-Expires` — token expiry, unix seconds (optional)
- `X-Sov-Seal` — an HMAC over the bundle (present only when sealing is enabled)

**Inter-service identity is trust-by-default.** A pod that opts in with
`WithTrustUpstreamClaims(true)` accepts these headers from its gateway with **no per-request
crypto** — the right model for a network-isolated mesh (the common case), and what keeps monolith,
hybrid, and mesh behaving identically with zero seal wiring. A pod that does NOT opt in (the default,
e.g. an edge gateway) **strips** all `X-Sov-*` identity headers so a client can't smuggle
`X-Sov-Subject: admin`.

**`X-Sov-Seal` is OPT-IN hardening for untrusted networks.** When the gateway↔pod link is not
network-isolated, enable the `hmacseal` plugin on both sides (keyed to your mesh secret — one secret,
no per-link wiring). The gateway then seals the bundle and the pod requires a valid seal, so a
network-present attacker who lacks the key can't forge or escalate identity. With no seal verifier
registered, a trusting pod accepts the bundle as-is (network is the trust boundary).

Where cryptographic verification belongs by default is the **edge** (client→gateway): the
`AuthService.verify` path and the optional Ed25519 request signing in `sov/signing`. The seal is for
the inter-service hop only, and only when you don't trust that hop's network.

### Seal format (when enabled)

Seal canonical form: collect **every** request header whose lowercased name starts with `x-sov-`
**except** `x-sov-seal`, as `lowercase_name=value\n` lines, **sorted by name**, concatenated:

```
seal = hex( HMAC_SHA256( seal_secret, "x-sov-expires=<v>\nx-sov-subject=<v>\n..." ) )
```

A pod verifying inbound identity recomputes this over the inbound `x-sov-*` set and compares to
`X-Sov-Seal`. If it fails (or is absent when a secret is configured), treat the request as
**anonymous** — do not honor `X-Sov-Subject`.

> **Fidelity trap:** the seal covers *every* `x-sov-*` header present at seal time, not just the four
> identity ones. If the gateway also stamped `x-sov-request-id` / `x-sov-upstream` before sealing,
> those are in the hash. Always hash the full `x-sov-*` set (minus `x-sov-seal`) you actually received.
> `sov conform --hmac-secret ...` pins this exactly.

When trust is **off** (edge gateway), the gateway **strips** all five identity headers before
dispatch, so a pod can never be tricked by a client smuggling `X-Sov-Subject: admin`. Other `X-Sov-*`
headers (`-Register-*`, `-Introspect-*`, `-Request-Id`, `-Upstream`) pass through.

---

## 5. `/rpc/_introspect` and `/rpc/_health`

### `/rpc/_introspect`

Source: `gateway/introspect.go` (`IntrospectReport`), `rpc/descriptor.go`,
`gateway/builtin/registry/aggregator.go` (cascade).

> **Opt-in.** The endpoint is **OFF by default** — the catalog discloses the
> full service/method/type surface, so the public endpoint is exposed only
> via `gw.Use(introspect.New())` (the `gateway/builtin/introspect` plugin). A
> gateway without it returns **404** on `/rpc/_introspect` (a closed endpoint
> looks absent, not "exists, wrong method").
>
> Consequences:
> - **Explorer** does NOT require it — the explorer builds the same report
>   in-process (`gateway.IntrospectBody`) and serves its own UI, so you can run
>   the explorer with the raw endpoint closed, or open the endpoint without the
>   explorer.
> - **Federation / mesh**: a registry aggregates pods by fetching each pod's
>   `/rpc/_introspect` over HTTP, so **every pod you want federated must use the
>   introspect plugin**. Without it the pod contributes nothing to the merged
>   catalog (the aggregator logs a warning).
> - **CLI** (`sov inspect` / `sov gen` / `sov conform` / `sov drift`) probe the
>   endpoint over HTTP, so the target gateway must have it enabled.
> - A polyglot pod implementing the wire contract by hand exposes
>   `/rpc/_introspect` directly; the opt-in toggle is a Go-gateway concept.

A pod returns its own catalog so the gateway can merge it. Minimal valid body:

```json
{
  "services": {
    "Chirp": [
      {
        "router": "Chirp",
        "title": "Chirp",
        "methods": [
          {
            "method": "post",
            "title": "Post",
            "postPath": "/rpc/Chirp/post",
            "hasParams": true,
            "params": [
              { "jsonName": "body", "schemaType": "string", "required": true, "position": 0 }
            ],
            "requestTypeScript": "",
            "responseTypeScript": ""
          }
        ]
      }
    ]
  },
  "types": {},
  "cross_refs": {}
}
```

`ParamField.schemaType ∈ {string, number, boolean, array, object}`; `position` is the positional slot
(`-1` for none); `typeName` names the Go type when `schemaType=="object"`. `types`/`cross_refs` may be
empty — the gateway rebuilds the org-wide catalog from merged `services`.

Cascade loop-guard headers (honor these if you fan out to further pods):

- `X-Sov-Introspect-Trace` — dedup id for diamond fan-outs.
- `X-Sov-Introspect-Visited` — comma-joined addresses already probed this round. Append your own
  address before fanning out; if your address is already present, short-circuit.

#### Hidden methods

A method can be hidden from the introspect catalog (explorer, codegen, federated peers). Source:
`rpc/engine.go` (markers), `rpc/fieldmap.go` (sentinel), `gateway/framework.go` (`stripHardHidden`).
Two levels:

- **Soft-hidden** — omitted from the default `/rpc/_introspect`, but returned (with
  `"internal": true` on the `MethodDescriptor`) when the request carries `X-Sov-Introspect-Internal: 1`.
  The explorer's "show internal" toggle uses this. Declare via the router marker
  `HiddenMethods() []string` (works for param-less methods) or a blank sentinel field on the params
  struct: ``_ struct{} `sov:"internal"` ``.
- **Hard-hidden** — stripped from *every* payload, including under `X-Sov-Introspect-Internal`, and
  never carries a wire flag. Applies automatically to the bound auth `verify` / authz `check`
  framework hooks; authors opt in via `HardHiddenMethods() []string` or ``_ struct{} `sov:"internal,hard"` ``.

The default report omits all hidden methods so a polyglot pod that simply doesn't advertise an
endpoint is wire-equivalent to a Go pod hiding it.

> **Security:** hiding removes *discoverability only*. A hidden endpoint is still live and
> dispatchable by anyone who knows the path — it is **not** an access-control boundary. Authz
> ([§6](#6-auth--authz-roles)), not hiding, governs who may call a method.

### `/rpc/_health`

Source: `gateway/framework.go` (`HealthReport`).

```json
{
  "status": "healthy",
  "checked_at": "2026-05-29T03:31:00Z",
  "gateway": { "status": "healthy" },
  "services": { "Chirp": { "status": "healthy", "local": true } }
}
```

Status taxonomy: `healthy | degraded | unhealthy | unknown | missing`. Return HTTP **200** when
healthy, **207** when degraded, **503** when unhealthy.

---

## 6. Auth / authz roles

Source: `gateway/auth.go` (`VerifyParams`, `CheckParams`, `AuthzDecision`).

A pod may optionally take the **auth** role (`auth:true` on register). The gateway routes every bearer
token to `POST /rpc/{name}/{verify}`:

```
request:  { "args": { "token": "<bearer>" } }
response: { "data": { "sub": "u_alice", "iss": "Auth", "scopes": ["mod"], "exp": 1735689600 } }
```

A non-empty `sub` means valid; any error means invalid (`401`). The gateway caches the result until `exp`.

A pod may take the **authz** role (`authz:true`). The gateway calls `POST /rpc/{name}/{check}` on
**every** request (including anonymous, where `claims` is null):

```
request:  { "args": { "claims": {…}|null, "service": "Chirp", "method": "delete" } }
response: { "data": { "allow": false, "reason": "mod role required", "authenticate": false } }
```

`allow:true` → proceed. `allow:false, authenticate:false` → `403`. `allow:false, authenticate:true` →
`401` (used to demand login for an anonymous caller).

Only one pod may hold each role; a second claimant gets `409 ROLE_CONFLICT` unless a takeover policy
plugin authorizes the change.

---

## 7. Minimum viable pod checklist

A non-role business pod (like Chirp) must:

1. **Register + heartbeat** — POST signed `/rpc/_register` on startup and every interval ([§2](#2-rpcregister--joining-the-mesh), [§3](#3-join-gates-control-plane)).
2. **Serve RPC** — accept `POST /rpc/{name}/{method}` in both arg shapes, return the data/error envelope ([§1](#1-rpc-request--response-envelope)).
3. **Serve `/rpc/_introspect`** — return your `services` catalog ([§5](#5-rpc_introspect-and-rpc_health)).
4. **Serve `/rpc/_health`** — return a health report ([§5](#5-rpc_introspect-and-rpc_health)).
5. **Read `X-Sov-*` identity** — to act as the authenticated caller (e.g. `X-Sov-Subject`), trust the
   bundle from your gateway. Verifying `X-Sov-Seal` is **optional** — add it only when the gateway↔pod
   network isn't trusted ([§4](#4-identity-propagation--the-optional-x-sov-seal)).

---

## 8. Conformance

`sov conform` validates a running pod against this contract end to end:

```
sov conform --pod http://localhost:9002 \
  --name Chirp \
  --mesh-secret demo-only-mesh-secret \
  --hmac-secret demo-only-secret \
  --method post --args '{"body":"hi"}'
```

It sends a signed register, decodes `/rpc/_introspect` and `/rpc/_health`, round-trips a method in both
arg shapes, and (with `--hmac-secret`) checks seal handling. Exit non-zero on any contract violation.
See `cmd/sov/conform/`.
