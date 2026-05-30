// Package signing implements Sov's per-request Ed25519 request-signing
// scheme. It is NOT an authentication verifier — it does not produce an
// authenticated user identity. It gates the request: a valid signature
// proves the caller holds the session keypair registered for X-Session.
// Identity (the "who is alice?" question) is the consumer's separate
// concern — typically a JWT middleware or a session lookup that runs
// before or after signing.
//
// Wire shape (set by client per request):
//
//	X-Session: <opaque session id>
//	X-Ts:      <unix seconds>
//	X-Sig:     <hex Ed25519 signature>
//
// Canonical signed payload (client and server must produce identical bytes):
//
//	v2\n<router>\n<method>\n<sha256_hex(body)>\n<unix_ts>\n
//
// Session lifecycle:
//
//   - The consumer exposes a session/init RPC method (in their own
//     router) that accepts a freshly generated client pubkey, allocates
//     a session id, persists the pubkey in a PublicKeyStore, and returns
//     the session id.
//   - Every subsequent RPC the client sends carries X-Session + X-Ts +
//     X-Sig. The Validator looks the session up, recomputes the
//     canonical message, and rejects on signature mismatch or stale ts.
//
// Replay window is 30s by default and configurable. Signatures are HEX
// (not base64) to match the wire format used in production prior art.
package signing
