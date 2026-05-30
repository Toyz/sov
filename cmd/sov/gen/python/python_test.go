package python

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/rpc"
)

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
							{JSONName: "handle", SchemaType: "string", Required: true, Position: 0,
								Title: "Handle", Desc: "Unique handle", Example: "alice"},
							{JSONName: "password", SchemaType: "string", Required: true, Position: 1},
						},
						RequestTypeScript:  "{ handle: string; password: string }",
						ResponseTypeScript: "{ token: string; subject: string }",
					},
					{
						// No-param method: HasParams false, no Params, and
						// (critically) NO "Auth.PingParams" entry in Types.
						// The generator must emit a no-arg method, never
						// reference an AuthPingParams type.
						Method: "ping", Title: "Ping", PostPath: "/rpc/Auth/ping", HasParams: false,
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
					{JSONName: "handle", SchemaType: "string", Required: true,
						Title: "Handle", Desc: "Unique handle", Example: "alice"},
					{JSONName: "password", SchemaType: "string", Required: true},
				},
			},
			// Presence-based optionality cases (Required is deliberately
			// false on every field to prove Optional is NOT driven by
			// Required):
			//   id        — non-omitempty non-pointer → REQUIRED (no Optional)
			//   note      — omitempty                 → optional
			//   parent_id — nullable (pointer)        → optional
			//   tags      — primitive-element array   → List[str]
			//   kids      — struct-element array, lowercase elem → List[Node]
			"Page": {
				Name:      "Page",
				ShapeHash: "jkl",
				Fields: []rpc.ParamField{
					{JSONName: "id", SchemaType: "string"},
					{JSONName: "note", SchemaType: "string", Omitempty: true},
					{JSONName: "parent_id", SchemaType: "string", Nullable: true},
					{JSONName: "tags", SchemaType: "array", ElemType: "string", Omitempty: true},
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

func TestRun_StdoutEmitsClient(t *testing.T) {
	s := startCatalogServer(t)
	var stdout, stderr bytes.Buffer
	code := Run([]string{"--from", s.URL, "--out", "-"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"class SovError(Exception):",
		"class SovClient:",
		"class Auth:",
		`return self._c.call("Auth", "login", p)`,
		"class App(SovClient):",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in generated client", want)
		}
	}
}

// Regression: a no-param method must emit a no-arg method and must NOT
// reference an undefined {Router}{Method}Params type.
func TestRun_NoParamMethodEmitsNoDanglingType(t *testing.T) {
	s := startCatalogServer(t)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--from", s.URL, "--out", "-"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "def ping(self) -> Any:") {
		t.Errorf("no-param method did not emit a no-arg method:\n%s", out)
	}
	if strings.Contains(out, "AuthPingParams") {
		t.Errorf("emitted a dangling AuthPingParams reference for a no-param method:\n%s", out)
	}
	// The Page.get method is also param-less.
	if !strings.Contains(out, "def get(self) -> Any:") {
		t.Errorf("zero-field-params method did not emit a no-arg method:\n%s", out)
	}
}

// Bug 1: a primitive-element array → List[str]; a struct-element array →
// List[Node] (named element, capitalized), never List[Any].
func TestRun_ArrayElementTyping(t *testing.T) {
	s := startCatalogServer(t)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--from", s.URL, "--out", "-"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "List[str]") {
		t.Errorf("primitive-element array not typed as List[str]:\n%s", out)
	}
	if !strings.Contains(out, "List[Node]") {
		t.Errorf("struct-element array not typed as List[Node]:\n%s", out)
	}
	if strings.Contains(out, "tags: Optional[List[Any]]") || strings.Contains(out, "kids: Optional[List[Any]]") {
		t.Errorf("array fields still emitted as List[Any]:\n%s", out)
	}
}

// Bug 3: optionality is presence-based, not Required-based. A non-omitempty
// non-pointer field is required even though Required=false; omitempty and
// pointer fields are Optional[...] = None.
func TestRun_PresenceBasedOptionality(t *testing.T) {
	s := startCatalogServer(t)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--from", s.URL, "--out", "-"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	// Present-always field → required, plain annotation, no Optional/default.
	if !strings.Contains(out, "    id: str\n") {
		t.Errorf("present-always field id not emitted as required `id: str`:\n%s", out)
	}
	// Anchor on the field-line indentation so this doesn't false-match
	// "parent_id: Optional[str]".
	if strings.Contains(out, "    id: Optional[str]") {
		t.Errorf("present-always field id wrongly emitted Optional — optionality must be presence-based:\n%s", out)
	}
	// omitempty → optional with default None.
	if !strings.Contains(out, "    note: Optional[str] = None\n") {
		t.Errorf("omitempty field note not emitted as Optional[str] = None:\n%s", out)
	}
	// nullable (pointer) → optional with default None.
	if !strings.Contains(out, "    parent_id: Optional[str] = None\n") {
		t.Errorf("nullable field parent_id not emitted as Optional[str] = None:\n%s", out)
	}
}

// Bug 3: name collision — a model whose ident equals a router ident must be
// suffixed Model, while the router class keeps the bare name; both coexist.
// Also: an unexported Go type name surfaces capitalized.
func TestRun_NameCollisionAndCapitalize(t *testing.T) {
	s := startCatalogServer(t)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--from", s.URL, "--out", "-"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"class PageModel:", // model colliding with router Page → PageModel
		"class Page:",      // router keeps the bare name
		"class Node:",      // unexported `node` → Node
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in generated client:\n%s", want, out)
		}
	}
	if strings.Contains(out, "class node:") {
		t.Errorf("unexported type emitted lowercase `class node`:\n%s", out)
	}
	// The colliding model must NOT define a second `class Page` dataclass.
	// Count occurrences of the exact dataclass decl that would collide.
	if strings.Contains(out, "@dataclass\nclass Page:") {
		t.Errorf("model wrongly emitted as `class Page` dataclass (collides with router):\n%s", out)
	}
}
