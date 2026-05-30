package kotlin

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
						// The generator must emit a no-arg fun, never reference
						// an AuthPingParams type.
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
			// Array-of-named-struct field: TypeName carries the element type
			// so codegen emits List<SearchHit>, not List<JsonElement>.
			"SearchResult": {
				Name:      "SearchResult",
				ShapeHash: "def",
				Fields: []rpc.ParamField{
					{JSONName: "hits", SchemaType: "array", TypeName: "SearchHit", Omitempty: true},
				},
			},
			"SearchHit": {
				Name:      "SearchHit",
				ShapeHash: "ghi",
				Fields: []rpc.ParamField{
					{JSONName: "id", SchemaType: "string", Required: true},
				},
			},
			// Presence-based optionality cases (Required is deliberately
			// false on every field to prove nullability is NOT driven by
			// Required):
			//   id        — non-omitempty non-pointer → REQUIRED (non-nullable)
			//   note      — omitempty                 → nullable T? = null
			//   parent_id — nullable (pointer)        → nullable T? = null
			"Page": {
				Name:      "Page",
				ShapeHash: "jkl",
				Fields: []rpc.ParamField{
					{JSONName: "id", SchemaType: "string"},
					{JSONName: "note", SchemaType: "string", Omitempty: true},
					{JSONName: "parent_id", SchemaType: "string", Nullable: true},
					// primitive-element array → List<String>
					{JSONName: "tags", SchemaType: "array", ElemType: "string", Omitempty: true},
					// struct-element array, element type lowercase → List<Node>
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
		"package com.example.sov",
		"open class SovClient(",
		"data class AuthLoginParams(",
		"@sample alice",
		"class Auth(private val c: SovClient) {",
		`c.call("Auth", "login", p)`,
		"class App(baseURL: String",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in generated client", want)
		}
	}
}

// Regression: a no-param method must emit a no-arg suspend fun and must
// NOT reference an undefined {Router}{Method}Params type.
func TestRun_NoParamMethodEmitsNoArgNotDanglingType(t *testing.T) {
	s := startCatalogServer(t)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--from", s.URL, "--out", "-"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "fun ping():") {
		t.Errorf("no-param method did not emit a no-arg fun:\n%s", out)
	}
	if strings.Contains(out, "AuthPingParams") {
		t.Errorf("emitted a dangling AuthPingParams reference for a no-param method:\n%s", out)
	}
}

// A slice-of-named-struct field must emit List<Elem> (via ParamField
// TypeName), not List<JsonElement> — so consumers don't have to cast.
func TestRun_ArrayOfStructFieldIsTyped(t *testing.T) {
	s := startCatalogServer(t)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--from", s.URL, "--out", "-"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "val hits: List<SearchHit>? = null") {
		t.Errorf("array-of-struct field not typed as List<SearchHit>:\n%s", out)
	}
	if strings.Contains(out, "val hits: List<JsonElement>") {
		t.Errorf("array-of-struct field still emitted as List<JsonElement>:\n%s", out)
	}
}

// Optionality is presence-based, not Required-based. A non-omitempty
// non-pointer field is non-nullable even though Required=false; omitempty
// and pointer fields are nullable T? = null.
func TestRun_PresenceBasedOptionality(t *testing.T) {
	s := startCatalogServer(t)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--from", s.URL, "--out", "-"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		`@SerialName("id") val id: String,`,                  // present-always → non-nullable, no default
		`@SerialName("note") val note: String? = null,`,      // omitempty → nullable
		`@SerialName("parent_id") val parent_id: String? = null,`, // pointer/nullable → nullable
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in generated client:\n%s", want, out)
		}
	}
	if strings.Contains(out, `val id: String? = null`) {
		t.Errorf("present-always field id emitted nullable — optionality must be presence-based:\n%s", out)
	}
}

// Generator polish: primitive arrays typed, unexported type names
// capitalized, and a type colliding with a router name suffixed (not
// declaring a duplicate class with the router).
func TestRun_GeneratorPolish(t *testing.T) {
	s := startCatalogServer(t)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--from", s.URL, "--out", "-"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"val tags: List<String>? = null",  // primitive-element array typed
		"val kids: List<Node>? = null",    // struct-element array, element capitalized
		"data class Node(",                // unexported `node` → Node
		"data class PageModel(",           // model colliding with router Page → PageModel
		"class Page(private val c: SovClient) {", // router keeps the bare name
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in generated client:\n%s", want, out)
		}
	}
	for _, notWant := range []string{
		"data class Page(",                // model must NOT take the bare router name
		"data class node(",                // not lowercase
		"val tags: List<JsonElement>",     // primitive array must not be JsonElement
		"val kids: List<JsonElement>",     // struct array must not be JsonElement
	} {
		if strings.Contains(out, notWant) {
			t.Errorf("unexpected %q in generated client:\n%s", notWant, out)
		}
	}
}

func TestRun_MissingFromFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected exit 2 for missing --from, got %d", code)
	}
}
