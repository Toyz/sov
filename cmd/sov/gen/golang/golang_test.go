package golang

import (
	"bytes"
	"encoding/json"
	"go/format"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/rpc"
)

// fakeCatalog mirrors the ts generator's regression catalog so the two
// emitters stay at parity: a no-param method, a HasParams-but-zero-fields
// method, primitive- and struct-element array fields (the struct element
// type name is lowercase), a Page model colliding with a Page router, and
// presence/nullable fields.
func fakeCatalog() gateway.IntrospectReport {
	return gateway.IntrospectReport{
		Services: map[string][]rpc.RouterDescriptor{
			"Auth": {{
				Router: "Auth",
				Title:  "Auth",
				Methods: []rpc.MethodDescriptor{
					{
						Method: "login", Title: "Login", PostPath: "/rpc/Auth/login", HasParams: true,
						Params: []rpc.ParamField{
							{JSONName: "handle", SchemaType: "string", Required: true, Position: 0},
							{JSONName: "password", SchemaType: "string", Required: true, Position: 1},
						},
						ResponseTypeScript: "{ token: string; subject: string }",
					},
					{
						// No-param method: HasParams false, no Params, and no
						// "Auth.PingParams" entry in Types. The generator must
						// emit a no-arg method, never reference AuthPingParams.
						Method: "ping", Title: "Ping", PostPath: "/rpc/Auth/ping", HasParams: false,
						ResponseTypeScript: "{ ok: boolean }",
					},
					{
						// Hostile/polyglot catalog: claims HasParams but ships
						// zero param fields. The generator must still emit no
						// arg (defensive len(Params) check), not a dangling type.
						Method: "legacy", Title: "Legacy", PostPath: "/rpc/Auth/legacy", HasParams: true,
						ResponseTypeScript: "{ ok: boolean }",
					},
				},
			}},
			// Router named "Page" → collides with the "Page" model type below.
			"Page": {{
				Router: "Page", Title: "Page",
				Methods: []rpc.MethodDescriptor{
					{Method: "get", Title: "Get", PostPath: "/rpc/Page/get", HasParams: false,
						ResponseTypeScript: "{ id: string }"},
				},
			}},
		},
		Types: map[string]gateway.TypeDescriptor{
			"Auth.LoginParams": {
				Name:      "Auth.LoginParams",
				ShapeHash: "abc",
				Fields: []rpc.ParamField{
					{JSONName: "handle", SchemaType: "string", Required: true},
					{JSONName: "password", SchemaType: "string", Required: true},
				},
			},
			// Array-of-named-struct field: TypeName carries the element type
			// so codegen emits []*SearchHit, not []any.
			"SearchResult": {
				Name:      "SearchResult",
				ShapeHash: "def",
				Fields: []rpc.ParamField{
					{JSONName: "hits", SchemaType: "array", ElemType: "object", TypeName: "SearchHit", Omitempty: true},
				},
			},
			"SearchHit": {
				Name:      "SearchHit",
				ShapeHash: "ghi",
				Fields: []rpc.ParamField{
					{JSONName: "id", SchemaType: "string", Required: true},
				},
			},
			// Presence/nullable cases (Required deliberately false to prove
			// pointer emission is NOT driven by Required):
			//   id        — non-omitempty non-pointer → plain string
			//   note      — omitempty                 → string + ,omitempty
			//   parent_id — nullable (pointer)        → *string
			"Page": {
				Name:      "Page",
				ShapeHash: "jkl",
				Fields: []rpc.ParamField{
					{JSONName: "id", SchemaType: "string"},
					{JSONName: "note", SchemaType: "string", Omitempty: true},
					{JSONName: "parent_id", SchemaType: "string", Nullable: true},
					// primitive-element array → []string
					{JSONName: "tags", SchemaType: "array", ElemType: "string", Omitempty: true},
					// struct-element array, element type lowercase → []*Node
					{JSONName: "kids", SchemaType: "array", ElemType: "object", TypeName: "node", Omitempty: true},
				},
			},
			// Unexported Go type name → must surface capitalized as Node.
			"node": {
				Name:      "node",
				ShapeHash: "nod",
				Fields:    []rpc.ParamField{{JSONName: "id", SchemaType: "string", Required: true}},
			},
		},
		CrossRefs: map[string]gateway.TypeVariants{},
	}
}

func startCatalogServer(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rpc/_introspect" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(fakeCatalog())
	}))
	t.Cleanup(s.Close)
	return s
}

// generate runs the emitter against the fake catalog and returns the
// generated Go source, failing the test on a non-zero exit code.
func generate(t *testing.T) string {
	t.Helper()
	s := startCatalogServer(t)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--from", s.URL, "--out", "-"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	return stdout.String()
}

// TestRun_OutputIsCompilableGo parses (and gofmts) the generated file so a
// regression that produces invalid Go fails loudly here.
func TestRun_OutputIsCompilableGo(t *testing.T) {
	out := generate(t)
	if _, err := parser.ParseFile(token.NewFileSet(), "client.go", out, parser.AllErrors); err != nil {
		t.Fatalf("generated Go does not parse: %v\n%s", err, out)
	}
	if _, err := format.Source([]byte(out)); err != nil {
		t.Fatalf("generated Go is not gofmt-able: %v\n%s", err, out)
	}
}

// TestRun_NoParamMethodNoDanglingParams: a no-param method emits a no-arg
// Go method and never references an undefined AuthPingParams type.
func TestRun_NoParamMethodNoDanglingParams(t *testing.T) {
	out := generate(t)
	if !strings.Contains(out, "Ping(ctx context.Context)") {
		t.Errorf("no-param method Ping did not emit a no-arg signature:\n%s", out)
	}
	if strings.Contains(out, "AuthPingParams") {
		t.Errorf("emitted a dangling AuthPingParams reference for a no-param method:\n%s", out)
	}
}

// TestRun_HasParamsButNoFieldsNoArg: a catalog claiming HasParams=true with
// zero fields must still emit a no-arg method, not a dangling params type.
func TestRun_HasParamsButNoFieldsNoArg(t *testing.T) {
	out := generate(t)
	if !strings.Contains(out, "Legacy(ctx context.Context)") {
		t.Errorf("HasParams+0-fields method Legacy did not emit a no-arg signature:\n%s", out)
	}
	if strings.Contains(out, "AuthLegacyParams") {
		t.Errorf("emitted dangling AuthLegacyParams for a 0-field method:\n%s", out)
	}
}

// TestRun_ArrayOfStructFieldIsTyped: a slice-of-named-struct field emits
// []*Elem (via ParamField TypeName), not []any.
func TestRun_ArrayOfStructFieldIsTyped(t *testing.T) {
	out := generate(t)
	if !strings.Contains(out, "Hits []*SearchHit `json:\"hits,omitempty\"`") {
		t.Errorf("array-of-struct field not typed as []*SearchHit:\n%s", out)
	}
	if strings.Contains(out, "Hits []any") {
		t.Errorf("array-of-struct field still emitted as []any:\n%s", out)
	}
}

// TestRun_NullableIsPointer: a nullable (pointer-source) field becomes *T;
// a present-always field stays plain; omitempty keeps the tag.
func TestRun_NullableIsPointer(t *testing.T) {
	out := generate(t)
	for _, want := range []string{
		"Id string `json:\"id\"`",                    // present-always → plain
		"Note string `json:\"note,omitempty\"`",      // omitempty → plain + tag
		"Parent_id *string `json:\"parent_id\"`",     // nullable → pointer
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in generated Go:\n%s", want, out)
		}
	}
	// Nullable must not be double-pointered, and present-always must not
	// become a pointer.
	if strings.Contains(out, "Id *string `json:\"id\"`") {
		t.Errorf("present-always field id wrongly emitted as pointer:\n%s", out)
	}
}

// TestRun_GeneratorPolish: primitive arrays typed, unexported type names
// capitalized, and a type colliding with a router name suffixed Model (not
// merged with the router interface).
func TestRun_GeneratorPolish(t *testing.T) {
	out := generate(t)
	for _, want := range []string{
		"Tags []string `json:\"tags,omitempty\"`", // primitive-element array typed
		"Kids []*Node `json:\"kids,omitempty\"`",  // struct-element array, element capitalized
		"type Node struct {",                      // unexported `node` → Node
		"type PageModel struct {",                 // model colliding with router Page → PageModel
		"type Page interface {",                   // router keeps the bare name
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in generated Go:\n%s", want, out)
		}
	}
	for _, notWant := range []string{
		"type Page struct {", // model must NOT take the bare router name
		"type node struct {", // not lowercase
		"Tags []any",         // primitive array must not be []any
		"Kids []any",         // struct array must not be []any
	} {
		if strings.Contains(out, notWant) {
			t.Errorf("unexpected %q in generated Go:\n%s", notWant, out)
		}
	}
}

func TestRun_MissingFromFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{}, &stdout, &stderr); code != 2 {
		t.Fatalf("expected exit 2 for missing --from, got %d", code)
	}
}
