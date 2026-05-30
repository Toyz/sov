package swift

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
						ResponseTypeScript: "LoginResult",
					},
					{
						// No-param method: HasParams false, no Params, and
						// (critically) NO "Auth.PingParams" entry in Types.
						// The generator must emit a no-arg method, never
						// reference an AuthPingParams struct.
						Method: "ping", Title: "Ping", PostPath: "/rpc/Auth/ping", HasParams: false,
						ResponseTypeScript: "void",
					},
					{
						// Hostile/polyglot catalog: claims HasParams but ships
						// zero param fields. The generator must still emit a
						// no-arg method (defensive len(Params) check), not a
						// dangling AuthLegacyParams reference.
						Method: "legacy", Title: "Legacy", PostPath: "/rpc/Auth/legacy", HasParams: true,
						ResponseTypeScript: "void",
					},
				},
			}},
			// Router named "Page" → collides with the "Page" model type below.
			"Page": {{
				Router: "Page", Title: "Page",
				Methods: []rpc.MethodDescriptor{
					{Method: "get", Title: "Get", PostPath: "/rpc/Page/get", HasParams: false,
						ResponseTypeScript: "void"},
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
			// false on every field to prove `?` is NOT driven by Required):
			//   id        — non-omitempty non-pointer → REQUIRED (no ?)
			//   note      — omitempty                 → optional
			//   parent_id — nullable (pointer)        → optional
			//   tags      — primitive-element array   → [String]
			//   kids      — struct-element array,
			//               lowercase element name    → [Node]
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
		"public enum Sov {",
		"public class SovClient {",
		"public struct AuthLoginParams: Codable {",
		"public final class Auth {",
		`try await client.call("Auth", "login", args: p)`,
		"public final class App: SovClient {",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in generated client", want)
		}
	}
}

// Regression: a no-param method must emit a no-arg method and must NOT
// reference an undefined {Router}{Method}Params struct.
func TestRun_NoParamMethodEmitsNoDanglingType(t *testing.T) {
	s := startCatalogServer(t)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--from", s.URL, "--out", "-"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "public func ping() async throws") {
		t.Errorf("no-param method did not emit a no-arg func:\n%s", out)
	}
	if strings.Contains(out, "AuthPingParams") {
		t.Errorf("emitted a dangling AuthPingParams reference for a no-param method:\n%s", out)
	}
}

// Defensive: a catalog that claims HasParams=true but ships zero param
// fields (e.g. a polyglot pod) must still emit a no-arg method, not a
// dangling params struct reference.
func TestRun_HasParamsButNoFieldsEmitsNoArg(t *testing.T) {
	s := startCatalogServer(t)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--from", s.URL, "--out", "-"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "public func legacy() async throws") {
		t.Errorf("HasParams+0-fields method did not emit a no-arg func:\n%s", out)
	}
	if strings.Contains(out, "AuthLegacyParams") {
		t.Errorf("emitted dangling AuthLegacyParams for a 0-field method:\n%s", out)
	}
}

// Bug 3: optionality is presence-based, not Required-based. A non-omitempty
// non-pointer field is required (no `?`) even though Required=false;
// omitempty and pointer fields become optional `T?`.
func TestRun_PresenceBasedOptionality(t *testing.T) {
	s := startCatalogServer(t)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--from", s.URL, "--out", "-"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"public var id: String\n",         // present-always → required, no ?
		"public var note: String?\n",      // omitempty → optional
		"public var parent_id: String?\n", // pointer/nullable → optional
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in generated client:\n%s", want, out)
		}
	}
	// id must NOT be optional.
	if strings.Contains(out, "public var id: String?") {
		t.Errorf("present-always field id emitted optional — optionality must be presence-based:\n%s", out)
	}
}

// Generator polish: primitive arrays typed, struct-element arrays typed
// with the capitalized element name, unexported type names capitalized,
// and a type colliding with a router name suffixed (Model) rather than
// redeclaring the router's class name.
func TestRun_GeneratorPolish(t *testing.T) {
	s := startCatalogServer(t)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--from", s.URL, "--out", "-"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"public var tags: [String]?\n",    // primitive-element array typed
		"public var kids: [Node]?\n",      // struct-element array, element capitalized
		"public struct Node: Codable {",   // unexported `node` → Node
		"public struct PageModel: Codable", // model colliding with router Page → PageModel
		"public final class Page {",        // router keeps the bare name
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in generated client:\n%s", want, out)
		}
	}
	for _, notWant := range []string{
		"public struct Page: Codable", // model must NOT take the bare router name
		"public struct node: Codable", // not lowercase
		"public var tags: [String: String]", // primitive array must not degrade
		"public var kids: [String]?",         // struct array must not be [String]
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
