package catalogcmd

import (
	"strings"
	"testing"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/rpc"
)

func TestCompareReports_DetectsBreaking(t *testing.T) {
	base := &gateway.IntrospectReport{
		Services: map[string][]rpc.RouterDescriptor{
			"User": {{Router: "User", Methods: []rpc.MethodDescriptor{{Method: "get"}, {Method: "delete"}}}},
		},
		Types: map[string]gateway.TypeDescriptor{
			"User": {Name: "User", Fields: []rpc.ParamField{
				{JSONName: "id", SchemaType: "string"},
				{JSONName: "age", SchemaType: "number"},
			}},
			"Gone": {Name: "Gone"},
		},
	}
	cur := &gateway.IntrospectReport{
		Services: map[string][]rpc.RouterDescriptor{
			// User.delete removed, User.list added
			"User": {{Router: "User", Methods: []rpc.MethodDescriptor{{Method: "get"}, {Method: "list"}}}},
		},
		Types: map[string]gateway.TypeDescriptor{
			"User": {Name: "User", Fields: []rpc.ParamField{
				{JSONName: "id", SchemaType: "number"}, // string -> number
				// age removed
			}},
			// Gone removed
			"New": {Name: "New"}, // added
		},
	}

	got := map[string]bool{} // msg -> breaking
	for _, c := range compareReports(base, cur) {
		got[c.msg] = c.breaking
	}

	for _, want := range []string{
		"removed method: User.delete",
		"removed type: Gone",
		"removed field: User.age",
	} {
		if b, ok := got[want]; !ok || !b {
			t.Errorf("want breaking %q (present=%v breaking=%v)", want, ok, b)
		}
	}
	changed := false
	for msg, b := range got {
		if b && strings.Contains(msg, "changed field type: User.id") {
			changed = true
		}
	}
	if !changed {
		t.Error("want breaking changed-field-type for User.id")
	}
	if got["added method: User.list"] {
		t.Error("added method must be non-breaking")
	}
	if got["added type: New"] {
		t.Error("added type must be non-breaking")
	}
}

func TestCompareReports_IdenticalIsCompatible(t *testing.T) {
	r := &gateway.IntrospectReport{
		Services: map[string][]rpc.RouterDescriptor{"A": {{Router: "A", Methods: []rpc.MethodDescriptor{{Method: "m"}}}}},
		Types:    map[string]gateway.TypeDescriptor{"T": {Name: "T", Fields: []rpc.ParamField{{JSONName: "f", SchemaType: "string"}}}},
	}
	for _, c := range compareReports(r, r) {
		if c.breaking {
			t.Errorf("identical reports must have no breaking change, got %q", c.msg)
		}
	}
}
