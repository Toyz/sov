# The `sov:` struct tag

A `Params` (or result) struct field is described by an optional `sov:` tag. The
tag drives the wire contract: positional slot, named wire key, validation
hints, human metadata, and where the value binds from. A field with no `sov:`
tag falls back to its `json:` tag (or a snake_cased field name).

## Grammar

```
sov:"<name>,<pos>,<flag|key=value>,..."
```

- **`<name>`** — the wire/JSON key (snake_case). Optional. Empty (a leading
  comma) keeps the json/snake_case fallback. `sov:"myname"` is "named-only, no
  positional slot".
- **`<pos>`** — the positional slot for `{"args":[...]}` dispatch (an integer,
  `0`-based). Optional. Positional slots must be contiguous `0..N-1` across the
  positional fields; header-bound and named-only fields consume no slot.
- **flags** (bare words): `omitempty`, `required`, `deprecated`.
  - `required` is a VALIDATION signal only — it does NOT imply wire presence.
    See [WIRE_CONTRACT.md](WIRE_CONTRACT.md) on optionality.
  - `required` and `omitempty` together is a build error.
- **key=value** metadata: `title=`, `desc=`, `doc=`, `example=` — human-facing,
  surfaced by the explorer and codegen JSDoc. Ignored by dispatch.
- **`header=<HeaderName>`** — bind this field from a request HEADER instead of
  the body. See [HEADER_PARAMS.md](HEADER_PARAMS.md).
- **`sov:"-"`** — exclude the field from the wire entirely.

On the blank sentinel field (`_ struct{} \`sov:"..."\``), method-level
directives: `internal` (soft-hide), `hard` (raise to hard-hide; requires
`internal`), and `perm=<token>` (declarative authz requirement, opaque to sov).

```go
type CreateParams struct {
    _        struct{} `sov:"perm=pages:write"`   // method-level authz
    Name     string   `sov:"name,0,required,title=Name,desc='The page name, unique'"`
    OwnerID  string   `sov:"owner_id,1,omitempty"`
    TenantID string   `sov:"header=X-Tenant-Id"` // bound from a header
    Secret   string   `sov:"-"`                  // never on the wire
}
```

## Commas and quotes in values

Human text (`title=`, `desc=`, `doc=`, `example=`) often contains commas. A bare
comma splits the tag, so it must be protected one of two ways:

- **Quote the value** (ergonomic): `sov:"desc='this, is my desc'"`. A comma
  between matched single quotes is literal. The quotes are stripped.
  - The opening quote must **immediately follow `=`** (no space): `desc='...'`,
    not `desc= '...'`.
  - A quoted value must be the WHOLE value — no trailing text after the closing
    quote (`desc='a'b` is a build error).
  - A quote that opens but never closes is a build error (fail loud).
- **Backslash-escape** the comma: `sov:"desc=a\,b"` → `a,b`. (In Go tag source,
  written `\\,` in a raw-string tag.)

A **plain apostrophe is a literal character** — it only acts as a quote at a
value start (right after `=`). So possessives and contractions just work and
never need escaping:

```go
`sov:"desc=User's Id,required"`        // desc="User's Id", required kept
`sov:"desc='a, b',title='X, Y'"`       // commas via quotes
`sov:"desc=isn\\'t"`                    // \' embeds a literal apostrophe
`sov:"desc='it isn\\'t, really'"`      // apostrophe + comma in one quoted value
```

Everything above is validated at boot (`Register`) — a malformed tag is a loud
panic with the field name, never a silent mangle.
