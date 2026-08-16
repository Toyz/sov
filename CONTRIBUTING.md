# Contributing to sov

Thanks for helping. sov aims to be a small, dependency-free, protocol-enforced
framework — contributions should keep it that way.

## Ground rules

- **Stdlib only.** sov has zero third-party runtime dependencies, on purpose
  (supply-chain control). Do not add a module `require` for framework or CLI
  code. `crypto/*`, `encoding/*`, `net/http`, `log/slog` etc. are all fair game.
  If you think you need a dependency, open an issue first — the answer is almost
  always "write the small piece we need."
- **The wire contract is the compatibility surface.** See
  [docs/VERSIONING.md](docs/VERSIONING.md). A change that removes or narrows a
  method, a param field, or a named type is breaking. Run `sov catalog diff`
  against a baseline when changing a service's shape.
- **Verify with the examples, not just unit tests.** A green `go test` is
  necessary, not sufficient — several real bugs (a batch concurrent-map crash, a
  scaffold boot panic, a plaintext-TLS hatch) were only caught by running the
  monolith + mesh examples. `bash examples/chirp/walkthrough.sh` must produce
  byte-equivalent output on monolith AND mesh.

## Before you open a PR

```sh
gofmt -l .        # must print nothing
go vet ./...      # must be clean
go test -race ./... -count=1
```

All three must pass. New behavior needs a test; a bug fix needs a test that
fails before the fix. Fuzz the tag parser (`go test ./rpc -run xxx -fuzz=Fuzz…`)
when touching `rpc/fieldmap.go`.

## Adding a builtin plugin

Builtins live under `gateway/builtin/<name>/`. Follow the existing shape:

- `func New(cfgs ...Config) *Plugin` — variadic, panics on `len(cfgs) > 1`.
- A `var (_ gateway.Plugin = (*Plugin)(nil); …)` block asserting every hook the
  plugin binds, so a signature drift is a build error, not a silent no-op.
- `PluginName()` and `Doc()` for introspect/explorer visibility.
- A disclosure-heavy or debug endpoint should self-gate on
  `gw.AuthBinding() != nil && req.User == nil` with an opt-in `Public bool`.

## Commits

- Conventional-ish prefixes (`feat`, `fix`, `refactor`, `docs`, `test`, `chore`)
  and a `(1.0)` scope while stabilizing the release.
- Do not bypass git hooks (`--no-verify` is blocked).
- Write the body as the change's rationale — what was broken and why this fixes
  it — not just what changed.

## Layout

- `rpc/` — the engine + tag grammar + descriptors (no HTTP).
- `gateway/` — the gateway, plugin hooks, mesh/registry, transports.
- `gateway/builtin/` — the plugins.
- `signing/` — Ed25519 request signing.
- `cmd/sov/` — the CLI; subcommand impls under `cmd/sov/internal/`.
- `examples/chirp/` — the reference app (monolith, hybrid, mesh, mesh-tiered).
- Anything under an `internal/` path is not public API.
