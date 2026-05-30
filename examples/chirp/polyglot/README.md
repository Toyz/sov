# Polyglot Chirp pod (Python)

`chirp_pod.py` is a **non-Go** mesh member: a pure-stdlib Python process that speaks the
[sov wire contract](../../../docs/WIRE_CONTRACT.md) and drops into the chirp mesh in place of the
Go `cmd/mesh/chirps` pod. Same gateway, same `walkthrough.sh`, equivalent output.

This is the **producer-polyglot** proof — sov's mesh is language-agnostic on the *producer* side,
not just the consumer side (generated clients). A pod in any language that implements the contract
is a first-class member: it signs its `/rpc/_register`, verifies the `X-Sov-Seal` identity bundle to
read the authenticated caller, and serves the dual-arg-shape RPC envelope, `/rpc/_introspect`, and
`/rpc/_health`.

## Run the mesh with the Python pod

Build the Go mesh binaries and the CLI once (gitignored output dir):

```sh
mkdir -p bin/out/mesh
for s in gateway auth authz users feed; do
  go build -o bin/out/mesh/$s ./examples/chirp/cmd/mesh/$s
done
go build -o bin/out/sov ./cmd/sov
```

Start the gateway + the four Go pods + the **Python** Chirp pod (same secrets everywhere):

```sh
MS=demo-only-mesh-secret; HS=demo-only-secret
SOV_LISTEN=:8080 SOV_HMAC_SECRET=$HS SOV_MESH_SECRET=$MS bin/out/mesh/gateway &
for p in "auth 9001" "authz 9005" "users 9003" "feed 9004"; do
  set -- $p
  SOV_LISTEN=:$2 SOV_ADVERTISE=http://localhost:$2 SOV_GATEWAY=http://localhost:8080 \
    SOV_HMAC_SECRET=$HS SOV_MESH_SECRET=$MS bin/out/mesh/$1 &
done
SOV_LISTEN=:9002 SOV_ADVERTISE=http://localhost:9002 SOV_GATEWAY=http://localhost:8080 \
  SOV_HMAC_SECRET=$HS SOV_MESH_SECRET=$MS python3 examples/chirp/polyglot/chirp_pod.py &
```

Wait ~6s for registration, then drive the standard walkthrough:

```sh
curl -s localhost:8080/rpc/_health | python3 -m json.tool   # Chirp shows source http://localhost:9002
BASE=http://localhost:8080 bash examples/chirp/walkthrough.sh
```

Steps 5/6 (post + timeline) and 14 (cascading batch) all run against the Python pod. `Chirp.post`
returns `author_id: u_bob` — proof the Python pod verified the seal and read the authenticated
subject. The gateway's batch plugin probes `/rpc/_batch` on the Python pod, gets 404, and falls back
to per-entry dispatch automatically (pods don't have to implement `_batch`).

## Validate conformance

```sh
bin/out/sov conform \
  --pod http://localhost:9002 --name Chirp \
  --gateway http://localhost:8080 \
  --mesh-secret demo-only-mesh-secret --hmac-secret demo-only-secret \
  --method post --args '{"body":"hi"}'
```

Exits 0 when the pod satisfies the contract (served introspect/health, dual-arg envelope, seal
handling, registered at the gateway, signing canon accepted).

## What this pod implements

| Obligation | Contract § | Code |
|---|---|---|
| Signed `/rpc/_register` + heartbeat | §2, §3 | `register_once` / `heartbeat_loop` |
| `X-Sov-Seal` verify → subject | §4 | `seal_subject` |
| Dual-arg-shape serve | §1 | `parse_args` |
| `/rpc/Chirp/*` handlers | §1 | `METHODS` |
| `/rpc/_introspect` | §5 | `INTROSPECT` |
| `/rpc/_health` | §5 | `health_body` |

Storage is in-memory; this is a demo, not a production pod.
