package rpc_test

import (
	"strings"
	"testing"

	"github.com/Toyz/sov/rpc"
)

// The filter DSL composes: capability (Implements) AND a name predicate, with
// Not/Or, all run through Find. Mirrors how a surface builtin selects the
// routers it serves.
func TestFilter_DSLCompose(t *testing.T) {
	e := rpc.NewEngine()
	e.Register(&AlphaRouter{}) // implements marker (embeds markImpl)
	e.Register(&BetaRouter{})  // plain

	// capability only
	if got := e.Find(rpc.Implements[marker]()); len(got) != 1 || got[0].Name != "Alpha" {
		t.Fatalf("Implements[marker] = %+v, want [Alpha]", got)
	}

	// capability AND name — Alpha matches capability but the name filter excludes it
	none := e.Find(rpc.And(
		rpc.Implements[marker](),
		rpc.ByName(func(n string) bool { return strings.HasPrefix(n, "Z") }),
	))
	if len(none) != 0 {
		t.Fatalf("And(cap, name=Z*) = %+v, want none", none)
	}

	// Or across two type names
	both := e.Find(rpc.Or(
		rpc.ByTypeName(func(tn string) bool { return tn == "AlphaRouter" }),
		rpc.ByTypeName(func(tn string) bool { return tn == "BetaRouter" }),
	))
	if len(both) != 2 {
		t.Fatalf("Or(Alpha,Beta) matched %d, want 2", len(both))
	}

	// Not inverts
	notAlpha := e.Find(rpc.Not(rpc.ByName(func(n string) bool { return n == "Alpha" })))
	if len(notAlpha) != 1 || notAlpha[0].Name != "Beta" {
		t.Fatalf("Not(name=Alpha) = %+v, want [Beta]", notAlpha)
	}

	// nil filter matches nothing
	if got := e.Find(nil); got != nil {
		t.Fatalf("Find(nil) = %+v, want nil", got)
	}
}
