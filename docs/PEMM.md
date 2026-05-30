# PEMM — the Protocol-Enforced Modular Monolith

PEMM is the architecture sov is built on. The name is the thesis:

> **Service boundaries are enforced by the wire protocol, not by code layout** — and the same
> boundary that enforces modularity is the one that lets a service move between in-process and
> remote without touching its code.

This document places PEMM in the design space, states what's genuinely new about it, and is honest
about what does the enforcing.

---

## The modular-monolith enforcement axis

Every "modular monolith" has to answer one question: **what stops module A from reaching into
module B's internals?** The answer is the real distinction between approaches — not the folder
structure. There are four levels of enforcement:

| Enforcement | The boundary is… | How hard to bypass | Examples |
|---|---|---|---|
| **Layout** | a folder / naming convention | trivial — just import it | most "modular monoliths", clean/hexagonal-by-folders |
| **Compile** | visibility & dependency rules | hard — the build fails | Go `internal/`, Java JPMS, ArchUnit, Rust crate privacy |
| **Runtime** | process / bundle isolation | very hard — separate runtimes | OSGi, Erlang/OTP, actor systems |
| **Protocol** ← **sov** | the call contract itself | you call `/rpc/{service}/{method}` either way | sov / PEMM |

Most projects that call themselves "modular monoliths" sit in the **Layout** row: the boundary is a
convention, and a developer one sprint from now will reach across it because nothing physically
stops them. The boundary is decorative, so when you later need to extract a service it's a rewrite.

PEMM sits in the **Protocol** row. In sov the *only* runtime path to another service is the same
request envelope you'd send over the network:

```
POST /rpc/{service}/{method}   { "args": ... }   →   { "data": ... } | { "error": ... }
```

The gateway's resolver hands a call either a **local dispatch** (in-process engine) or a **remote
proxy** (HTTP) — both behind the identical `Call(service, method, args)` contract. There is no
in-process back-channel: you cannot share a pointer, reach a private field, or call an unexported
method across a service, because the protocol is the only door and there is no second door.

---

## The genuinely new part

Uniform local/remote calls alone are **not** new — that's *location transparency*, and Erlang/OTP,
Akka, and Google's Service Weaver all have a version of it. PEMM's distinct contribution is what the
boundary is made to do:

> **The boundary that ENFORCES modularity is the SAME boundary that ENABLES relocation.**

One mechanism, two payoffs that are usually in tension:

- **Modularity is enforced** — because crossing a service edge *means* speaking the protocol, A
  literally cannot couple to B's internals.
- **Mono ↔ mesh is free** — because that same protocol is already transport-agnostic, moving B to
  its own pod is a deploy/config change, not a code change. The call site is unchanged.

Compare the usual options:

- A **layout-enforced monolith** gives you modularity on paper but a decorative boundary, so
  splitting later is a rewrite.
- **Microservices** give you a real boundary but you pay the network tax *always*, from day one,
  for every call.

PEMM makes the **enforcement boundary** and the **deployment seam** the same line. You get the
real boundary of microservices and the in-process speed of a monolith, and you choose per-service,
reversibly, at deploy time. That's the new point in the space — see
[`docs/WIRE_CONTRACT.md`](WIRE_CONTRACT.md) for the boundary's exact shape and
[`BENCHMARKS.md`](../BENCHMARKS.md) for what each side costs (in-process ≈ µs, remote = 1 RTT).

---

## Honesty: what actually does the enforcing

"Protocol-enforced" is precise about **runtime** and needs one discipline to be precise about
**compile time** too:

- **At runtime** it is fully enforced: the only way across a service edge is `/rpc/...`. The
  resolver returns a local dispatch or a remote proxy; there is no shared-memory shortcut.
- **At compile time** the guarantee is only as strong as your package discipline: **one service per
  package, and services do not import each other's types.** The chirp example follows this rule
  explicitly — *"Auth and User share the subject as their only common key; neither imports the
  other's types."* Go will not, by itself, stop a developer from `import`-ing a sibling service's
  package and calling its struct method directly, bypassing the protocol.

To make the compile-time side airtight rather than conventional, put each service's internals behind
an `internal/` package (or a separate module) so sibling services physically cannot reference the
types — then the compiler enforces "the protocol is the only path", and PEMM is enforced by
construction, not by code review.

| | Runtime | Compile time |
|---|---|---|
| Enforced by | the resolver (no in-process back-channel) | package discipline → make literal with `internal/` |
| Status today | enforced | sanctioned by convention; harden with `internal/` |

---

## Lineage (so the claim is defensible)

PEMM's ancestry is **location transparency** (Erlang/OTP, Akka) and the single-binary-splittable
model (Google Service Weaver, Lagom). What none of those market — and what PEMM names — is the
*fusion of the modularity-enforcement boundary with the deployment boundary*, expressed as a plain
HTTP/JSON protocol with a live, introspectable contract (`/rpc/_introspect`), implementable by a pod
in **any language** (see the polyglot proof in `examples/chirp/polyglot/`). The contract being the
boundary is also what lets a non-Go process be a first-class module — something a
compile/runtime-enforced approach can't offer, because their boundary isn't portable across
languages.

---

## One-line definition

> **PEMM: a modular monolith whose module boundaries are the RPC protocol itself — enforced because
> there is no other way to call across a service, and free to relocate because that protocol is
> transport-agnostic.**
