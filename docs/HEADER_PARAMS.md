# Header-bound params

Status: IMPLEMENTED on `feat/header-params` (off `feat/mesh-core`).

Bind a param struct field from a request **header** instead of the request
body:

```go
type CreateParams struct {
    Name     string `json:"name"`                       // body (codec-decoded)
    TenantID string `sov:"header=X-Tenant-Id"`           // ambient header
    ReqID    string `sov:"header=X-Request-Id,required"` // header, must be present
}
```

The field's value comes from the named inbound header, not the `{"args": ...}`
envelope. Everything else about the method is unchanged — it still serves
`/rpc`, still meshes, still reflects into the explorer / MCP / codegen.

This is `@Header`-style binding (Spring `@RequestHeader`, FastAPI `Header(...)`,
ASP.NET `[FromHeader]`) reified in sov's reflected-metadata model.

## Why

Cross-cutting request metadata — tenant id, request/trace id, locale, API
version — that many handlers want as a typed param, without every handler
reaching into `rc.State` by hand and re-parsing. Declarative, reflected, and
opaque: sov binds the string and never interprets it.

## Tag syntax

```
sov:"header=<HeaderName>"            // optional; zero value if absent
sov:"header=<HeaderName>,required"   // 400 if the header is absent/empty
```

- `<HeaderName>` is the literal HTTP header, canonicalized on lookup (the
  existing `Header.Get` is already case-insensitive — see
  [server.go](../gateway/server.go)).
- `required` reuses the existing validation directive; here it also gates wire
  presence of the header (absent/empty → `BadRequest`).
- A field is either a body field OR a header field, never both. `header=` and a
  `json:` wire name on the same field is a build error (ambiguous source).

### Build-time validation (all fail loud at boot)

A `header=` field is rejected by `BuildFieldMap` / `Register` when it:

- names the reserved **`X-Sov-*`** namespace (case-insensitive) — the verified
  claims channel is off-limits to user params;
- is **not a scalar** (string/bool/int/uint/float, or a pointer to one) — a
  header is a single string;
- **duplicates** another field's header name (case-insensitive);
- has an empty name, or also carries a `json:` wire name;
- sits on a **nested OR embedded struct field** rather than a direct field of
  the top-level params struct — sov does not flatten embedded structs, so a
  header field one level down is never bound at runtime and, because such
  structs decode via a `snake_case` body key, would otherwise be settable from
  the request body while the schema shows it absent (a spoofing vector).
  Enforced by `RejectNestedHeaders` at registration. Declare `header=` fields
  directly on each params struct; a shared mixin is not supported.

Header fields also consume no positional slot, so one may sit anywhere among
ordinary body fields without breaking positional (`{"args":[...]}`) dispatch.

## Binding pipeline

The core constraint: the **rpc engine stays HTTP-agnostic**. It never learns
about "headers" — it reads a generic string getter from the context bag that
the gateway populated from the inbound headers.

```
inbound Request
      │
      ▼
gateway.dispatchLocal
  rc.Set(rpc.CtxHeaderGetter, req.Header.Get)   // one getter, gateway-owned
      │
      ▼
engine.Dispatch
  1. codec.DecodeParams(body, ptr, fieldMap)    // BODY fields only (unchanged)
  2. bindHeaderFields(ptr, fieldMap, rc)         // NEW: header fields, post-decode
      │                                          //   getter := rc.Get(CtxHeaderGetter)
      ▼                                          //   for each FieldInfo.HeaderSource != "":
  handler(rc, params)                            //     set field from getter(name)
```

Touch points:

- `rpc` defines `CtxHeaderGetter` (state key) and the getter type
  `type HeaderGetter func(name string) string`. Gateway sets it; no
  `rpc → gateway` dependency (gateway already imports `rpc`).
- [rpc/fieldmap.go](../rpc/fieldmap.go): parse `header=NAME` →
  `FieldInfo.HeaderSource string` (binding-facing).
- rpc dispatch (`rpc/dispatch.go`): the new `bindHeaderFields` pass, run after
  the codec decode, before the handler call.
- [gateway/dispatch.go](../gateway/dispatch.go) `dispatchLocal`: stash the
  getter alongside the existing `rc.Set(ContextKey...)` calls.

Binding sits **outside** the codec on purpose. The codec owns the body wire
(JSON, or a bring-your-own binary codec); header binding is orthogonal to it, so
a custom codec neither sees nor re-encodes header fields. This is enforced, not
merely conventional: `bindHeaderFields` **zeroes** each header field before
binding, so even a BYO codec that ignores the `FieldMap` and unmarshals the
whole struct (which could otherwise set a header field from the body) cannot
break the "body OR header, never both" rule.

## Mesh transparency (free)

`dispatchRemote` forwards every inbound header verbatim; only `X-Sov-*` claim
headers are stripped (anti-smuggling for identity). A custom `X-Tenant-Id`
therefore **survives the remote hop**, so a header-param binds identically
whether the service is local, an in-process `LinkPeer`, or a remote pod. PEMM
holds with zero surface-specific code — same as any other param.

Verified against [gateway/dispatch.go](../gateway/dispatch.go) `dispatchRemote`
(forwards `req.Header` as-is) and the `X-Sov-*`-only strip in the server
adapter.

Batch note: `/rpc/_batch` and `/rpc/_batchstream` carry ONE header set for the
whole call, cloned onto each entry — so a header= param on a batched method
binds the same value for every entry (the wire protocol has no per-entry header
override). A required header absent from the batch request 400s each entry that
declares it, independently.

## Descriptor and schema honesty (hard rule)

A header field is NOT a body field. It MUST be excluded from every JSON
body/args schema, or the surfaces lie:

- **MCP** `tools/call` `inputSchema` — an LLM sends `arguments` as JSON; it
  cannot set an arbitrary HTTP header through a tool argument. A header-param
  must not appear as a required arg. It binds instead from the MCP client's own
  forwarded header (MCP `callTool` already clones `mcpReq.Header` into the
  sub-request — see [tools.go](../gateway/builtin/mcp/tools.go)).
- **OpenAPI / `/rpc` request body** — header params belong in the operation's
  `parameters: [{in: header}]`, never in the request body schema.
- **Codegen (TS/Swift clients)** — a header-param is not a field of the request
  body type. MVP: omit it from the body type; a client sets it via a transport
  interceptor. (A later pass could surface a typed `headers` argument.)

Implementation: `ParamField` (descriptor) gains `Source string` (`""` = body,
`"header"` = header) and `Header string` (the name). `inputSchema` /
`BuildTypeCatalog` / codegen skip `Source == "header"` fields for the body
schema and surface them separately. See the optionality note in
[WIRE_CONTRACT.md](WIRE_CONTRACT.md) — header presence is a THIRD presence
axis, distinct from body optionality and `required` validation.

## Security model (loudest edge)

A header is **caller-controlled, untrusted input**. Binding is plumbing, not
trust.

- NEVER use a header-param for identity or authorization. Identity is `Claims`
  (auth middleware). A header-bound `tenant` must STILL be authorized against
  `Claims` by the handler or a `MethodAuthorizer` — the binding does not confer
  trust.
- The `X-Sov-*` claim namespace is reserved (verified claims travel there
  between trusted nodes). `header=` in that namespace is a **build error**
  (case-insensitive) — a param can never read or shadow the claim channel,
  regardless of a node's `TrustUpstreamClaims` wiring.
- Because header-params are untrusted at every boundary, they suit the
  re-verify-at-each-hop posture: the value is data to be checked, never a
  standing grant.
- **Binding is pre-parser.** A header= param binds the header state captured at
  gateway INGRESS — before any `HeaderParser` plugin mutates `req.Header` — so a
  bound value always matches what the `AuthzService.Check` gate saw (Check also
  runs on the pre-parser headers). A parser that rewrites/canonicalizes a header
  therefore cannot make the handler's param diverge from what was authorized. If
  a handler wants a parser's normalized value, read it from `rc.State` (where
  parsers stash), not from a header= param.
- **Framework-managed header names are topology-dependent.** Some header names
  are set by the framework itself: `dispatchRemote` overwrites `X-Forwarded-For`
  with the edge's resolved `RemoteIP`, while an in-process `LinkPeer` hop leaves
  the caller's value intact. A `header=X-Forwarded-For` param thus resolves to
  different values depending on how the service is reached — avoid binding
  framework-injected header names unless that topology dependence is intended.

Docs for the feature must state this at the top, not in a footnote.

## Type conversion

- `string` — native, no conversion.
- Scalars (`int`, `int64`, `bool`, `float64`) — parsed with the same conversion
  the field map already applies; a parse failure on a `required` field is a
  `BadRequest`, on an optional field leaves the zero value (or errors — open
  question below).
- Structs/slices/maps from a header — OUT of scope. A header is a single
  scalar-ish string; complex shapes stay body fields.

## Decisions (resolved)

- **Optional-and-unparseable fails loud.** An ABSENT optional header leaves the
  zero value; a PRESENT header that fails to parse to the field's scalar type is
  a `BadRequest` (400) whether or not the field is `required`. A malformed value
  the caller DID send is an error, not a silent zero.
- **Multi-value headers** bind the comma-joined string (`Header` is comma-joined
  today). No `[]string` split.
- **`from=query:` / `from=path:`** are deliberately not in the tag. The syntax is
  the literal `header=NAME`; a generalized `from=<src>:<name>` is a future call
  if more sources appear.
- **Response headers (binding OUT)** are not this feature — inbound only.

## Implementation map

- Tag parse + `FieldInfo.HeaderSource` + `FieldMap.HeaderFields`; build-error on
  `header=` + `json:` collision and on an empty header name — `rpc/fieldmap.go`.
- Bind pass + `rpc.CtxHeaderGetter` / `HeaderGetter` contract + scalar coercion —
  `rpc/header.go`; called from both the reflect path (`rpc/dispatch.go`) and the
  reflection-free `Handle` fast path (`rpc/handle.go`, gated on `HeaderFields`).
- Gateway captures a pre-parser header snapshot at `handle` ingress and
  populates the getter from it in `dispatchLocal` — both gated on
  `Engine.NeedsHeaderGetter()` (boot-computed), so an all-body deployment pays
  no snapshot/alloc — `gateway/dispatch.go`, `rpc/engine.go`.
- Header-only methods take no body argument: `MethodDescriptor.HasBodyParams()`
  (`rpc/descriptor.go`) gates the type catalog and all five generators, so a
  method whose only params are headers emits `params: void`, not a required
  empty struct.
- `rpctest.WithHeader(name, value)` sets a header for handler unit tests
  (`rpctest/rpctest.go`).
- `ParamField.Source`/`Header` (`rpc/descriptor.go`), set in
  `describeFieldMap` (`rpc/schema.go`). These ride the `/rpc/_introspect`
  payload, so every introspection consumer sees the split:
  - MCP `inputSchema` excludes header fields (`gateway/builtin/mcp/tools.go`).
  - The type catalog — and every descriptor-based codegen (ts/swift/kotlin/
    python/go) — excludes them (`gateway/typecatalog.go` `withoutHeaderParams`),
    since a header field is not part of a type's JSON shape.
  - The explorer's live TS render omits them (`rpc/tsrender/render.go`).
  - The explorer UI renders header params in the method's Parameters table with
    a "header" badge and the header name (not a blank body row), gives them
    dedicated inputs under a "Headers" block in Try-it, keeps them OUT of the
    args JSON, and sends them as real HTTP headers on execute
    (`gateway/builtin/explorer/static/{app.js,style.css}`).

The invariant across surfaces: `md.Params` carries a header field (flagged
`Source="header"`, no JSON name) so a method's inputs are fully described;
`report.Types[...].Fields` (the JSON shape) does not.

Tests: `rpc/header_test.go` (field map, reflect + Handle bind, scalar/required,
Describe marks Source), `gateway/header_param_test.go` (end-to-end + LinkPeer
mesh hop + introspect type/param split), `gateway/builtin/mcp/mcp_test.go`
(schema exclusion + bound-from-forwarded-header).
