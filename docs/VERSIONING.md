# Versioning + compatibility policy

sov follows [semantic versioning](https://semver.org). This document states what
"compatible" means for sov — which surfaces are covered by the semver guarantee,
and how changes are staged.

## The wire contract is the primary compatibility surface

sov's whole value is that generated clients in five languages, the OpenAPI spec,
and the MCP tool surface all describe **one** wire contract. That contract — not
the Go API — is what most consumers depend on:

- `POST /rpc/{Router}/{method}` with body `{"args": [ <params> ]}` (positional)
  or `{"args": { <fields> }}` (named).
- Success: `{"data": <result>}`. Failure: `{"error": {"code", "message", ...}}`.
- Framework endpoints under `/rpc/_*` (`_health`, `_ready`, `_introspect`,
  `_manifest`, `_batch`, `_register`) and the `/mcp` surface.

Within a **major** version:
- A method, its params' fields, or a named type is **never removed or narrowed**
  in a minor/patch release. Removing a method or field, or changing a field's
  type, is a **breaking** change (major only).
- New methods, new optional fields, and new types are **minor** additions.
- Use `sov catalog snapshot` + `sov catalog diff` in CI to enforce this against a
  committed golden catalog — a breaking delta fails the build.

## Go API stability

These packages are the stable, semver-covered Go API:

| Package | Surface |
|---|---|
| `github.com/Toyz/sov` | Top-level façade (`New`, `NewMonolith`, …, `Context`, error constructors, `ShutdownContext`, `LoadEnv`). |
| `github.com/Toyz/sov/gateway` | `Gateway`, `Options`/`WithX`, the plugin hook interfaces, `Request`/`Response`, `DispatchEvent`. |
| `github.com/Toyz/sov/gateway/builtin/*` | Each builtin's `Config` + `New`. |
| `github.com/Toyz/sov/rpc` | `Engine`, `Context`, `Error` + constructors, the descriptor types, the `sov:` tag grammar. |
| `github.com/Toyz/sov/signing` | Request-signing validator + store interface. |
| `github.com/Toyz/sov/rpctest` | Handler test helpers. |

Anything under an `internal/` path (e.g. `cmd/sov/internal/...`,
`gateway/internal/...`) is **not** public API and may change at any time.

## Deprecation

A field carries `sov:"...,deprecated"`; a method is marked deprecated on its
`_` sentinel (`sov:"_,deprecated=use Foo.bar instead"`). Both surface in
`/rpc/_introspect`, the OpenAPI spec, and generated-client doc comments, and a
deprecated method may emit a `Deprecation` / `Sunset` response header. A
deprecated symbol is kept for at least one minor release before removal in the
next major.

## Pre-1.0

Before `v1.0.0`, breaking changes may land in a minor bump, batched per wave and
recorded in [CHANGELOG.md](../CHANGELOG.md). The guarantees above become binding
at `v1.0.0`.
