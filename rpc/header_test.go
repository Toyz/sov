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
