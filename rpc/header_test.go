package rpc

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

type HdrRouter struct{}

type hdrParams struct {
	Tenant string `sov:"header=X-Tenant-Id"`
}

func (HdrRouter) Who(_ *Context, p *hdrParams) (string, error) { return p.Tenant, nil }

type hdrReqParams struct {
	ReqID string `sov:"header=X-Request-Id,required"`
}

func (HdrRouter) Need(_ *Context, p *hdrReqParams) (string, error) { return p.ReqID, nil }

type hdrIntParams struct {
	Count int `sov:"header=X-Count"`
}

func (HdrRouter) Cnt(_ *Context, p *hdrIntParams) (int, error) { return p.Count, nil }

type hdrMixParams struct {
	Name   string `json:"name"`
	Tenant string `sov:"header=X-Tenant-Id"`
}

func (HdrRouter) Mix(_ *Context, p *hdrMixParams) (string, error) {
	return p.Name + "@" + p.Tenant, nil
}

func ctxWithHeaders(h map[string]string) *Context {
	rc := NewContext(context.Background())
	rc.Set(CtxHeaderGetter, HeaderGetter(func(name string) string { return h[name] }))
	return rc
}

// A header= field is kept OUT of the body wire maps and collected into
// HeaderFields; a sibling body field is unaffected.
func TestHeaderParam_FieldMapExcludesFromBody(t *testing.T) {
	fm, err := BuildFieldMap(reflect.TypeOf(hdrMixParams{}))
	if err != nil {
		t.Fatalf("BuildFieldMap: %v", err)
	}
	if _, ok := fm.ByName["tenant"]; ok {
		t.Fatalf("header field leaked into body ByName: %v", fm.ByName)
	}
	if _, ok := fm.ByName["name"]; !ok {
		t.Fatalf("body field 'name' missing from ByName: %v", fm.ByName)
	}
	if len(fm.HeaderFields) != 1 {
		t.Fatalf("HeaderFields = %v, want exactly 1", fm.HeaderFields)
	}
	if hs := fm.Fields[fm.HeaderFields[0]].HeaderSource; hs != "X-Tenant-Id" {
		t.Fatalf("HeaderSource = %q, want X-Tenant-Id", hs)
	}
}

// A field is body OR header, never both: header= alongside a json wire name is
// a build error.
func TestHeaderParam_JSONNameCollisionIsBuildError(t *testing.T) {
	type bad struct {
		X string `json:"x" sov:"header=X-Foo"`
	}
	_, err := BuildFieldMap(reflect.TypeOf(bad{}))
	if err == nil || !strings.Contains(err.Error(), "body OR header") {
		t.Fatalf("expected body-or-header collision error, got %v", err)
	}
}

func TestHeaderParam_EmptyNameIsBuildError(t *testing.T) {
	type bad struct {
		X string `sov:"header="`
	}
	_, err := BuildFieldMap(reflect.TypeOf(bad{}))
	if err == nil || !strings.Contains(err.Error(), "empty header name") {
		t.Fatalf("expected empty-header-name error, got %v", err)
	}
}

// The reflect dispatch path binds a header field from the context getter.
func TestHeaderParam_BindsFromHeader(t *testing.T) {
	e := NewEngine()
	e.Register(&HdrRouter{})
	rc := ctxWithHeaders(map[string]string{"X-Tenant-Id": "acme"})
	status, body := e.Dispatch(rc, "Hdr", "who", nil)
	if status != 200 || !strings.Contains(string(body), `"acme"`) {
		t.Fatalf("status=%d body=%s", status, body)
	}
}

// A required header that is absent is a 400.
func TestHeaderParam_RequiredAbsentIs400(t *testing.T) {
	e := NewEngine()
	e.Register(&HdrRouter{})
	rc := ctxWithHeaders(map[string]string{}) // no X-Request-Id
	status, _ := e.Dispatch(rc, "Hdr", "need", nil)
	if status != 400 {
		t.Fatalf("required-absent header: status=%d, want 400", status)
	}
}

// Scalar header fields parse; a bad scalar is a 400.
func TestHeaderParam_ScalarParse(t *testing.T) {
	e := NewEngine()
	e.Register(&HdrRouter{})

	rc := ctxWithHeaders(map[string]string{"X-Count": "42"})
	status, body := e.Dispatch(rc, "Hdr", "cnt", nil)
	if status != 200 || !strings.Contains(string(body), "42") {
		t.Fatalf("good int header: status=%d body=%s", status, body)
	}

	rc2 := ctxWithHeaders(map[string]string{"X-Count": "notanint"})
	status2, _ := e.Dispatch(rc2, "Hdr", "cnt", nil)
	if status2 != 400 {
		t.Fatalf("bad int header: status=%d, want 400", status2)
	}
}

// Body and header fields bind together on the same params struct.
func TestHeaderParam_MixedBodyAndHeader(t *testing.T) {
	e := NewEngine()
	e.Register(&HdrRouter{})
	rc := ctxWithHeaders(map[string]string{"X-Tenant-Id": "acme"})
	status, body := e.Dispatch(rc, "Hdr", "mix", []byte(`{"args":{"name":"bob"}}`))
	if status != 200 || !strings.Contains(string(body), `"bob@acme"`) {
		t.Fatalf("status=%d body=%s", status, body)
	}
}

// Describe marks a header field with Source="header" and the header name, and
// gives it no JSON wire name — the metadata the explorer/introspection surface
// renders it from.
func TestHeaderParam_DescribeMarksSource(t *testing.T) {
	e := NewEngine()
	e.Register(&HdrRouter{})
	var mixParams []ParamField
	for _, rd := range e.Describe() {
		if rd.Router != "Hdr" {
			continue
		}
		for _, m := range rd.Methods {
			if m.Method == "mix" {
				mixParams = m.Params
			}
		}
	}
	if mixParams == nil {
		t.Fatal("Hdr.mix not described")
	}
	var hdr, body *ParamField
	for i := range mixParams {
		if mixParams[i].Source == "header" {
			hdr = &mixParams[i]
		} else {
			body = &mixParams[i]
		}
	}
	if hdr == nil {
		t.Fatalf("header param absent from Describe: %+v", mixParams)
	}
	if hdr.Header != "X-Tenant-Id" || hdr.JSONName != "" {
		t.Fatalf(`header ParamField = %+v, want Header="X-Tenant-Id" JSONName=""`, *hdr)
	}
	if body == nil || body.JSONName != "name" {
		t.Fatalf("body param 'name' missing/wrong: %+v", body)
	}
}

// The reflection-free Handle fast path binds header fields too.
func TestHeaderParam_HandleFastPath(t *testing.T) {
	e := NewEngine()
	Handle(e, "H", "who", func(_ *Context, p *hdrParams) (string, error) { return p.Tenant, nil })
	rc := ctxWithHeaders(map[string]string{"X-Tenant-Id": "acme"})
	status, body := e.Dispatch(rc, "H", "who", nil)
	if status != 200 || !strings.Contains(string(body), `"acme"`) {
		t.Fatalf("Handle header bind: status=%d body=%s", status, body)
	}
}

// A header= in the reserved X-Sov-* verified-claims namespace is a build error
// (case-insensitive) — a user param must never read the claim channel.
func TestHeaderParam_RejectsReservedXSovNamespace(t *testing.T) {
	type bad struct {
		S string `sov:"header=X-Sov-Subject"`
	}
	if _, err := BuildFieldMap(reflect.TypeOf(bad{})); err == nil || !strings.Contains(err.Error(), "X-Sov-") {
		t.Fatalf("expected reserved-namespace error, got %v", err)
	}
	type badLower struct {
		S string `sov:"header=x-sov-issuer"`
	}
	if _, err := BuildFieldMap(reflect.TypeOf(badLower{})); err == nil {
		t.Fatalf("reserved namespace must be case-insensitive; lowercase x-sov- accepted")
	}
}

// A non-scalar header= field is rejected at build time, not deferred to a
// first-request 400. Pointer-to-scalar is allowed.
func TestHeaderParam_RejectsNonScalarType(t *testing.T) {
	type inner struct{ X string }
	type badStruct struct {
		M inner `sov:"header=X-Meta"`
	}
	type badSlice struct {
		T []string `sov:"header=X-Tags"`
	}
	type badMap struct {
		M map[string]string `sov:"header=X-Map"`
	}
	for _, rt := range []reflect.Type{reflect.TypeOf(badStruct{}), reflect.TypeOf(badSlice{}), reflect.TypeOf(badMap{})} {
		if _, err := BuildFieldMap(rt); err == nil || !strings.Contains(err.Error(), "scalar") {
			t.Fatalf("expected scalar-type error for %s, got %v", rt, err)
		}
	}
	type okPtr struct {
		N *int `sov:"header=X-Count"`
	}
	if _, err := BuildFieldMap(reflect.TypeOf(okPtr{})); err != nil {
		t.Fatalf("pointer-to-scalar header should build, got %v", err)
	}
}

// Two fields binding from the same header is a build error (case-insensitive).
func TestHeaderParam_RejectsDuplicateHeaderName(t *testing.T) {
	type bad struct {
		A string `sov:"header=X-Tenant-Id"`
		B string `sov:"header=x-tenant-id"`
	}
	if _, err := BuildFieldMap(reflect.TypeOf(bad{})); err == nil || !strings.Contains(err.Error(), "duplicate sov header") {
		t.Fatalf("expected duplicate-header error, got %v", err)
	}
}

type nestedHdrMeta struct {
	TenantID string `sov:"header=X-Tenant-Id"`
}

type nestedHdrParams struct {
	Note string        `json:"note"`
	Meta nestedHdrMeta `json:"meta"`
}

type NestedHdrRouter struct{}

func (NestedHdrRouter) Go(_ *Context, p *nestedHdrParams) (string, error) { return p.Note, nil }

// A header= on a NESTED struct field (never bound, and body-spoofable) fails
// loud at registration.
func TestHeaderParam_NestedHeaderRejectedAtRegister(t *testing.T) {
	if err := RejectNestedHeaders(reflect.TypeOf(nestedHdrParams{})); err == nil || !strings.Contains(err.Error(), "nested") {
		t.Fatalf("RejectNestedHeaders should reject a nested header field, got %v", err)
	}
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Register with a nested header field should panic")
		}
	}()
	NewEngine().Register(&NestedHdrRouter{})
}

// A required header field must NOT be enforced by the BODY required-check
// (which keys on the empty wire name and would 400 "field \"\" is required"
// even when the header IS present). bindHeaderFields enforces it instead. This
// path (named-object / empty-array args) is what MCP tools/call always sends.
func TestHeaderParam_RequiredHeaderWithNamedObjectArgs(t *testing.T) {
	e := NewEngine()
	e.Register(&HdrRouter{}) // Need has ReqID sov:"header=X-Request-Id,required"

	rc := ctxWithHeaders(map[string]string{"X-Request-Id": "r1"})
	// header PRESENT + named-object body → 200 (body check must skip the header field)
	if status, body := e.Dispatch(rc, "Hdr", "need", []byte(`{"args":{}}`)); status != 200 || !strings.Contains(string(body), `"r1"`) {
		t.Fatalf(`required header present + {"args":{}}: status=%d body=%s`, status, body)
	}
	// header PRESENT + explicit empty array → 200
	if status, _ := e.Dispatch(rc, "Hdr", "need", []byte(`{"args":[]}`)); status != 200 {
		t.Fatalf(`required header present + {"args":[]}: status=%d`, status)
	}
	// header ABSENT → 400 from bindHeaderFields (right message), not "field \"\" is required"
	rc2 := ctxWithHeaders(map[string]string{})
	status, body := e.Dispatch(rc2, "Hdr", "need", []byte(`{"args":{}}`))
	if status != 400 || !strings.Contains(string(body), "missing required header") {
		t.Fatalf("required header absent: status=%d body=%s (want 400 missing required header)", status, body)
	}
}

// The reserved-namespace check trims the extracted name, so a space-padded
// X-Sov-* can't slip past; a padded normal header binds by its trimmed name.
func TestHeaderParam_ReservedNamespaceTrimsWhitespace(t *testing.T) {
	type bad struct {
		S string `sov:"header= X-Sov-Subject"`
	}
	if _, err := BuildFieldMap(reflect.TypeOf(bad{})); err == nil || !strings.Contains(err.Error(), "X-Sov-") {
		t.Fatalf("space-padded reserved header must still be rejected, got %v", err)
	}
	type ok struct {
		T string `sov:"header= X-Tenant-Id "`
	}
	fm, err := BuildFieldMap(reflect.TypeOf(ok{}))
	if err != nil {
		t.Fatalf("padded header build: %v", err)
	}
	if got := fm.Fields[fm.HeaderFields[0]].HeaderSource; got != "X-Tenant-Id" {
		t.Fatalf("header name not trimmed: %q", got)
	}
}

type EmbedHdrBase struct {
	TenantID string `sov:"header=X-Tenant-Id"`
}

type embedHdrParams struct {
	EmbedHdrBase
	Note string `json:"note"`
}

// An exported embedded struct carrying a header field is body-spoofable (sov
// decodes it under a snake_case key, it does not promote) — rejected at build,
// with a message that names embedding.
func TestHeaderParam_EmbeddedHeaderRejected(t *testing.T) {
	err := RejectNestedHeaders(reflect.TypeOf(embedHdrParams{}))
	if err == nil || !strings.Contains(err.Error(), "embedded") {
		t.Fatalf("embedded header field should be rejected mentioning embedding, got %v", err)
	}
}

// HasBodyParams is false for a header-only method (no body arg) and true when a
// body field is present — the signal codegen gates the request-body argument
// on, so a header-only method emits a no-arg call.
func TestHeaderParam_HasBodyParams(t *testing.T) {
	e := NewEngine()
	e.Register(&HdrRouter{})
	got := map[string]bool{}
	for _, rd := range e.Describe() {
		if rd.Router != "Hdr" {
			continue
		}
		for _, m := range rd.Methods {
			got[m.Method] = m.HasBodyParams()
		}
	}
	if got["who"] {
		t.Fatalf("who is header-only → HasBodyParams should be false")
	}
	if !got["mix"] {
		t.Fatalf("mix has a body field → HasBodyParams should be true")
	}
}

// NeedsHeaderGetter is false until a method with a header= param is registered.
func TestHeaderParam_NeedsHeaderGetterFlag(t *testing.T) {
	plain := NewEngine()
	plain.Register(&DualRouter{}) // no header fields
	if plain.NeedsHeaderGetter() {
		t.Fatalf("engine with no header params should not NeedHeaderGetter")
	}
	withHdr := NewEngine()
	withHdr.Register(&HdrRouter{})
	if !withHdr.NeedsHeaderGetter() {
		t.Fatalf("engine with a header param should NeedHeaderGetter")
	}
}

type posHdrParams struct {
	A string `json:"a"`
	T string `sov:"header=X-Tenant-Id"`
	B string `json:"b"`
}

type PosHdrRouter struct{}

func (PosHdrRouter) Go(_ *Context, p *posHdrParams) (string, error) {
	return p.A + "|" + p.B + "|" + p.T, nil
}

// An interspersed header= field must not desync positional auto-numbering: an
// ordinary struct with a header field between two untagged body fields
// registers and still dispatches positionally.
func TestHeaderParam_PositionalNotDesyncedByHeaderField(t *testing.T) {
	e := NewEngine()
	e.Register(&PosHdrRouter{}) // must NOT panic on the contiguity check
	rc := ctxWithHeaders(map[string]string{"X-Tenant-Id": "acme"})
	status, body := e.Dispatch(rc, "PosHdr", "go", []byte(`{"args":["av","bv"]}`))
	if status != 200 || !strings.Contains(string(body), "av|bv|acme") {
		t.Fatalf("positional+header dispatch: status=%d body=%s", status, body)
	}
}
