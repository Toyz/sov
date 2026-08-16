package mock

import (
	"bytes"
	"go/format"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/rpc"
)

func fakeCatalog() gateway.IntrospectReport {
	return gateway.IntrospectReport{
		Services: map[string][]rpc.RouterDescriptor{
			"Auth": {{
				Router: "Auth", Title: "Auth",
				Methods: []rpc.MethodDescriptor{
					{Method: "login", PostPath: "/rpc/Auth/login", ResponseTypeName: "LoginResult"},
					{Method: "tail", PostPath: "/rpc/Auth/tail", Streaming: true, ResponseTypeName: "Event"},
					{Method: "ping", PostPath: "/rpc/Auth/ping"}, // no response type -> empty map
				},
			}},
		},
		Types: map[string]gateway.TypeDescriptor{
			"LoginResult": {Name: "LoginResult", Fields: []rpc.ParamField{
				{JSONName: "token", SchemaType: "string", Example: "abc"},
				{JSONName: "subject", SchemaType: "string"},
				{JSONName: "count", SchemaType: "number", Example: "3"},
				{JSONName: "ok", SchemaType: "boolean", Example: "true"},
				{JSONName: "roles", SchemaType: "array"},
			}},
			"Event": {Name: "Event", Fields: []rpc.ParamField{
				{JSONName: "line", SchemaType: "string"},
			}},
		},
	}
}

func TestEmit_ProducesValidGofmtableGo(t *testing.T) {
	var buf bytes.Buffer
	rep := fakeCatalog()
	Emit(&buf, "main", &rep)
	out := buf.String()

	if _, err := parser.ParseFile(token.NewFileSet(), "main.go", out, parser.AllErrors); err != nil {
		t.Fatalf("generated mock is not valid Go: %v\n%s", err, out)
	}
	if _, err := format.Source([]byte(out)); err != nil {
		t.Fatalf("generated mock is not gofmt-able: %v", err)
	}

	for _, want := range []string{
		// Type is "{Service}Router" so the engine registers it under "Auth".
		"type AuthRouter struct{}",
		"func (AuthRouter) Login(_ *rpc.Context) (map[string]any, error)",
		"func (AuthRouter) Tail(_ *rpc.Context) (rpc.Stream[map[string]any], error)",
		"func (AuthRouter) Ping(_ *rpc.Context) (map[string]any, error)",
		`"token": "abc"`, // string example carried through
		`"count": 3`,     // numeric example carried through
		`"ok": true`,     // boolean example carried through
		"rpc.StreamSlice(",
		"gw.Register(&AuthRouter{})",
		"rpcsurface.New()",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("generated mock missing %q\n%s", want, out)
		}
	}
}
