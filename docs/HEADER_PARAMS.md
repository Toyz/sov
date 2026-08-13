# Header-bound params (design)

Status: DESIGN — not implemented. Proposed for a branch off `feat/mesh-core`.

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
a custom codec neither sees nor re-encodes header fields.

## Mesh transparency (free)

`dispatchRemote` forwards every inbound header verbatim; only `X-Sov-*` claim
headers are stripped (anti-smuggling for identity). A custom `X-Tenant-Id`
therefore **survives the remote hop**, so a header-param binds identically
whether the service is local, an in-process `LinkPeer`, or a remote pod. PEMM
holds with zero surface-specific code — same as any other param.

Verified against [gateway/dispatch.go](../gateway/dispatch.go) `dispatchRemote`
(forwards `req.Header` as-is) and the `X-Sov-*`-only strip in the server
adapter.

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
- The `X-Sov-*` claim namespace is reserved and edge-stripped; header-params
  cannot read it (and must not try to smuggle identity through a side header).
- Because header-params are untrusted at every boundary, they suit the
  re-verify-at-each-hop posture: the value is data to be checked, never a
  standing grant.

Docs for the feature must state this at the top, not in a footnote.

## Type conversion

- `string` — native, no conversion.
- Scalars (`int`, `int64`, `bool`, `float64`) — parsed with the same conversion
  the field map already applies; a parse failure on a `required` field is a
  `BadRequest`, on an optional field leaves the zero value (or errors — open
  question below).
- Structs/slices/maps from a header — OUT of scope. A header is a single
  scalar-ish string; complex shapes stay body fields.

## Non-goals / open questions

- `from=query:` / `from=path:` — deliberately not in the tag now. The chosen
  syntax is the literal `header=NAME`; a generalized `from=<src>:<name>` is a
  future call if more sources appear.
- Optional-and-unparseable: does a bad scalar in a NON-required header error, or
  silently zero? Leaning error (fail loud), but it is a decision.
- Multi-value headers: `Header` is comma-joined today; a header-param binds the
  joined string. No `[]string` split in MVP.
- Response headers (binding OUT) — not this feature. This is inbound only.

## Phased implementation (when approved)

1. `fieldmap.go` tag parse + `FieldInfo.HeaderSource`; build-error on
   `header=` + `json:` collision. Unit tests on the field map.
2. rpc dispatch `bindHeaderFields` pass + `rpc.CtxHeaderGetter` / `HeaderGetter`
   contract. Engine-level bind tests (string + scalar + required-absent).
3. gateway `dispatchLocal` sets the getter. Local end-to-end test; mesh test
   proving the header binds across a remote hop.
4. `ParamField.Source`/`Header` + introspect/`inputSchema` exclusion; MCP tool
   schema honesty test (header field absent from `inputSchema`, still binds from
   the forwarded header).
5. Codegen: omit header fields from body types; doc the interceptor pattern.
6. `docs/` usage section + the security caveat up top.
