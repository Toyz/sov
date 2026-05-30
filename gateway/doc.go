// Package gateway is the entry point that wraps a sov rpc.Engine and
// exposes it over an HTTP-shaped server. The package owns the HTTP
// boundary; the rpc package itself is transport-free.
//
// The HTTP server is pluggable via the Server interface. The default
// implementation uses net/http and is suitable for production. Consumers
// who want fiber / fasthttp / echo / a custom server implement Server
// themselves and pass it via WithServer(...). Users do NOT depend on a
// fiber adapter shipped by sov.
//
// A Gateway wraps:
//
//   - rpc.Engine — services hosted IN-PROCESS (the modular-monolith case)
//   - Resolver chain — services resolved to remote endpoints (the
//     microservice case). LocalResolver consults the in-process engine;
//     RegisterResolver consults the TTL-backed map populated by
//     POST /rpc/_register.
//
// Both can coexist per request: the gateway tries each resolver in order.
// In-process services dispatch via direct Engine.Dispatch; remote
// services HTTP-proxy with strip+inject of X-Sov-* claim headers. The
// wire shape is identical in either path — same encoder, same auth, same
// observability, same error model. That symmetry is the PEMM thesis.
//
// Framework endpoints — always present, gateway-owned:
//
//   - GET  /rpc/_health         aggregated health rollup
//   - GET  /rpc/_introspect     aggregated rpc.Engine.Describe() across services
//
// Builtin-plugin endpoints — present when the plugin is registered via
// gw.Use(...), not part of the bare gateway:
//
//   - POST /rpc/_register       service self-registration + heartbeat TTL (builtin/registry)
//   - POST /rpc/_batch          object-keyed fan-in across multiple methods (builtin/batch)
//
// Service-level `_X` paths (e.g. /rpc/WidgetService/_debug) are refused
// at the gateway with 404; those are reachable only via direct
// intra-cluster addressing.
package gateway
