package rpc

import (
	"strings"
	"testing"
)

type ListReq struct {
	Limit  int      `json:"limit,omitempty"`
	Cursor string   `json:"cursor,omitempty"`
	Tags   []string `json:"tags,omitempty"`
}

type Widget struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type WidgetListResult struct {
	Items []Widget `json:"items"`
	Next  string   `json:"next,omitempty"`
}

type WidgetRouter struct{}

func (r *WidgetRouter) List(ctx *Context, p *ListReq) (*WidgetListResult, error) {
	return &WidgetListResult{}, nil
}

func (r *WidgetRouter) Get(ctx *Context, p *struct {
	ID string `json:"id"`
}) (*Widget, error) {
	return &Widget{}, nil
}

// EmptyParams is a params struct with zero wire fields — e.g. a method
// whose inputs all arrive via context/headers, or an intentional
// placeholder. The Go signature takes *EmptyParams (so the engine's
// hasParams is true and dispatch still allocates it), but the client sends
// nothing on the wire.
type EmptyParams struct{}

type NoFieldRouter struct{}

// Tree takes a params struct with no wire fields.
func (r *NoFieldRouter) Tree(ctx *Context, p *EmptyParams) (*Widget, error) { return &Widget{}, nil }

// Ping takes no params struct at all.
func (r *NoFieldRouter) Ping(ctx *Context) (*Widget, error) { return &Widget{}, nil }

// Regression: a method taking a zero-wire-field params struct must report
// HasParams=false. Otherwise the type catalog (emits a request type only
// when len(Params) > 0) and every codegen backend (references
// {Router}{Method}Params whenever HasParams) disagree, and the generated
// client names a params interface that was never emitted — a dangling
// reference that won't compile. (Bug: `sov gen ts` against a no-arg method.)
func TestDescribe_ZeroFieldParamsIsNotHasParams(t *testing.T) {
	e := NewEngine()
	e.Register(&NoFieldRouter{})
	out := e.Describe()
	if len(out) != 1 {
		t.Fatalf("routers = %d", len(out))
	}
	for _, m := range out[0].Methods {
		if m.HasParams {
			t.Errorf("%s: HasParams=true with %d wire fields; a zero-field params struct must report HasParams=false", m.Method, len(m.Params))
		}
		if len(m.Params) != 0 {
			t.Errorf("%s: expected 0 params, got %#v", m.Method, m.Params)
		}
	}
}

func TestDescribe_BuildsRouterAndMethodInfo(t *testing.T) {
	e := NewEngine()
	e.Register(&WidgetRouter{})
	out := e.Describe()
	if len(out) != 1 || out[0].Router != "Widget" || out[0].Title != "Widget" {
		t.Fatalf("describe[0] = %#v", out[0])
	}
	if len(out[0].Methods) != 2 {
		t.Fatalf("methods = %d", len(out[0].Methods))
	}
}

func TestDescribe_ParamFieldsRespectJSONTag(t *testing.T) {
	e := NewEngine()
	e.Register(&WidgetRouter{})
	out := e.Describe()

	var list MethodDescriptor
	for _, m := range out[0].Methods {
		if m.Method == "list" {
			list = m
		}
	}
	if list.Method != "list" {
		t.Fatal("list method missing")
	}
	if !list.HasParams || len(list.Params) != 3 {
		t.Fatalf("params = %#v", list.Params)
	}
	// All three are omitempty → required false
	for _, p := range list.Params {
		if p.Required {
			t.Fatalf("field %s should be optional: %#v", p.JSONName, p)
		}
	}
	// tags is an array → schema_type=array, designer hint = "List of text values"
	var tags ParamField
	for _, p := range list.Params {
		if p.JSONName == "tags" {
			tags = p
		}
	}
	if tags.SchemaType != "array" || !strings.Contains(tags.DesignerHint, "text") {
		t.Fatalf("tags = %#v", tags)
	}
}

func TestDescribe_TypeScriptPreview(t *testing.T) {
	e := NewEngine()
	e.Register(&WidgetRouter{})
	out := e.Describe()
	var list MethodDescriptor
	for _, m := range out[0].Methods {
		if m.Method == "list" {
			list = m
		}
	}
	if !strings.Contains(list.RequestTypeScript, "limit?: number") {
		t.Fatalf("request preview = %q", list.RequestTypeScript)
	}
	if !strings.Contains(list.RequestTypeScript, "tags?: string[]") {
		t.Fatalf("request preview = %q", list.RequestTypeScript)
	}
	if !strings.Contains(list.ResponseTypeScript, "items: ") || !strings.Contains(list.ResponseTypeScript, "[]") {
		t.Fatalf("response preview = %q", list.ResponseTypeScript)
	}
}

func TestHumanize(t *testing.T) {
	cases := map[string]string{
		"List":          "List",
		"ListInvoices":  "List invoices",
		"GetAPIKey":     "Get API key",
		"WorkspaceSlug": "Workspace slug",
	}
	for in, want := range cases {
		if got := OperationTitle(in); got != want {
			t.Errorf("OperationTitle(%q) = %q, want %q", in, got, want)
		}
	}
}
