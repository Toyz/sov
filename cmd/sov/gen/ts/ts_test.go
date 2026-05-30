package ts

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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
						// The generator must emit `params: void` + a no-arg
						// method, never reference an AuthPingParams type.
						Method: "ping", Title: "Ping", PostPath: "/rpc/Auth/ping", HasParams: false,
						ResponseTypeScript: "{ ok: boolean }",
					},
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
		"export namespace sov {",
		"export class SovClient",
		"export interface AuthLoginParams {",
		"@example alice",
		"export class Auth {",
		`return this.c.call("Auth", "login", p)`,
		"export class App extends SovClient {",
		"readonly Auth = new Auth(this);",
		// batch surface
		"export type BatchResult<T = unknown>",
		"async batch<T extends Record<string, BatchEntry>>",
		"/rpc/_batch",
		// typed batch via Services augmentation
		"export type AuthLoginResult",
		"export interface Services {",
		"login: { params: AuthLoginParams; result: AuthLoginResult }",
		// runtime base URL helpers
		"setBaseURL(url: string)",
		"get baseURL()",
		// typed single-entry invoke
		"async invoke<E extends BatchEntry>",
		"export type ResultOf<E>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in generated client", want)
		}
	}
}

// Regression: a no-param method must emit `params: void` and a no-arg
// method, and must NOT reference an undefined {Router}{Method}Params type.
// (Bug: `sov gen ts` against a method taking a zero-field params struct
// referenced AuthDeleteAccountParams/PageTreeParams that were never
// emitted → the client didn't compile.)
func TestRun_NoParamMethodEmitsVoidNotDanglingType(t *testing.T) {
	s := startCatalogServer(t)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--from", s.URL, "--out", "-"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"ping: { params: void; result: AuthPingResult }", // Services catalog
		"async ping(): Promise<AuthPingResult>",          // no-arg router method
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in generated client", want)
		}
	}
	if strings.Contains(out, "AuthPingParams") {
		t.Errorf("emitted a dangling AuthPingParams reference for a no-param method:\n%s", out)
	}
}

func TestRun_MissingFromFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected exit 2 for missing --from, got %d", code)
	}
}

func TestRun_RejectsBothFromAndExec(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"--from", "http://x", "--exec", "/bin/true"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected exit 2 for both flags set, got %d", code)
	}
	if !strings.Contains(stderr.String(), "exactly one") {
		t.Fatalf("expected mutex error, got stderr=%q", stderr.String())
	}
}

func TestRun_ExecSpawnsAndGenerates(t *testing.T) {
	if testing.Short() {
		t.Skip("--exec spawn test does a real go build; skip in -short mode")
	}
	// Build the chirp monolith into a tempfile so sovgen can exec it.
	bin := t.TempDir() + "/sov-monolith"
	build := exec.Command("go", "build", "-o", bin, "github.com/Toyz/sov/examples/chirp/cmd/monolith")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("go build chirp monolith: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Run([]string{"--exec", bin, "--out", "-"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exec run exited %d, stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"export namespace sov {",
		"export interface Services {",
		"export class Auth {",
		"export class Chirp {",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("--exec output missing %q", want)
		}
	}
}

func TestRun_DriftWarning(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		report := fakeCatalog()
		report.CrossRefs["User"] = gateway.TypeVariants{
			Name: "User",
			Variants: []gateway.TypeVariant{
				{ShapeHash: "a1", Services: []string{"Auth"}, Fields: []rpc.ParamField{{JSONName: "id", SchemaType: "string"}}},
				{ShapeHash: "b2", Services: []string{"User"}, Fields: []rpc.ParamField{{JSONName: "id", SchemaType: "string"}, {JSONName: "extra", SchemaType: "string"}}},
			},
		}
		_ = json.NewEncoder(w).Encode(report)
	}))
	defer s.Close()
	var stdout, stderr bytes.Buffer
	code := Run([]string{"--from", s.URL, "--out", "-"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if !strings.Contains(stderr.String(), `drift detected — type "User"`) {
		t.Fatalf("missing drift warning, stderr=%q", stderr.String())
	}
}
