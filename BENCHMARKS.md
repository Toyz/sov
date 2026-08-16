# Sov benchmarks — the cost of PEMM

PEMM's claim is that the **same handler** is addressable whether it runs in-process or across the
mesh, and you choose the topology at deploy time. The natural question: *what does that cost?* These
benchmarks answer it.

## Headline

| Path | What it measures | ns/op | vs local |
|---|---|---:|---:|
| `EngineDispatchLocal` | raw rpc engine dispatch (reflection + JSON + bind), no gateway | ~2.0 µs | — |
| `HandleLocal` | full gateway dispatch to an **in-process** service | ~1.6 µs | 1× |
| `HandleRemote` | the same call to a **remote** pod — one HTTP round trip (loopback) | ~38 µs | ~24× |
| `BatchCoalesce` | **5** calls to one remote pod in a single `/rpc/_batch` | ~70 µs | — |

**The three things to take away:**

1. **In-process call ≈ a function call.** A local PEMM dispatch is **~1.6 µs** — reflection +
   envelope decode + field bind. No network, no serialization-to-the-wire-and-back. When a service
   lives in your binary, calling it costs microseconds.

2. **Remote = exactly one round trip.** The same call to a remote pod is **~38 µs on loopback** —
   and that figure is *dominated by the HTTP round trip*, not by sov. On a real network the delta is
   your RTT (sub-millisecond same-AZ, single-digit ms cross-AZ). The framework adds the envelope, not
   a second hop.

3. **Batch coalesces N→1.** Five calls to the same remote pod cost **~70 µs as one batch** versus
   **~5 × 38 µs ≈ 190 µs** dispatched individually — because the gateway collapses them into **one**
   nested `/rpc/_batch` POST (1 round trip, not 5). The benchmark asserts the collapse (`hits == 1`).
   On a real network where RTT dominates, this is the difference between 1×RTT and N×RTT.

So the PEMM tax for keeping a service *movable* is microseconds in-process; going remote costs you a
round trip you'd pay with any RPC system, and batching gives most of it back when you fan out to one
destination.

## Reproduce

```sh
go test -bench=. -benchmem -run='^$' ./rpc/ ./gateway/
```

Benchmarks: `rpc/bench_test.go` (`EngineDispatchLocal`) and `gateway/bench_test.go`
(`HandleLocal`, `HandleRemote`, `BatchCoalesce`).

Numbers above were captured on the maintainer's machine (Linux, Go 1.26, 32-thread). Absolute ns/op
is hardware-specific — what's stable is the *ratio* between local, remote, and batched.

Run them yourself with `make bench`. There is no automated regression gate — microbenchmarks are
too noisy on shared CI runners to gate on; compare runs manually on the same machine when you change
a hot path.
