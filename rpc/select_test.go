package rpc_test

import (
	"testing"

	"github.com/Toyz/sov/rpc"
)

// marker is a sealed capability: satisfiable only by embedding markImpl (its
// method is unexported), mirroring how the mcp built-in defines ToolRouter.
type markImpl struct{}

func (markImpl) mark() {}

type marker interface{ mark() }

type AlphaRouter struct{ markImpl }

func (AlphaRouter) Ping(ctx *rpc.Context) (string, error) { return "a", nil }

type BetaRouter struct{}

func (BetaRouter) Ping(ctx *rpc.Context) (string, error) { return "b", nil }

// Select is the general seam: match by Go type name OR by a marker-interface
// assertion on the live Value.
func TestSelect_ByPredicate(t *testing.T) {
	e := rpc.NewEngine()
	e.Register(&AlphaRouter{})
	e.Register(&BetaRouter{})

	byType := e.Select(func(ri rpc.RouterInfo) bool { return ri.TypeName == "BetaRouter" })
	if len(byType) != 1 || byType[0].Name != "Beta" {
		t.Fatalf("select by TypeName = %+v, want [Beta]", byType)
	}

	byIface := e.Select(func(ri rpc.RouterInfo) bool { _, ok := ri.Value.(marker); return ok })
	if len(byIface) != 1 || byIface[0].Name != "Alpha" {
		t.Fatalf("select by marker = %+v, want [Alpha]", byIface)
	}
}

// Notes has no "Router" suffix — it must still register (the suffix is a
// convention for a clean wire name, not a hard requirement) under its full type
// name.
type Notes struct{}

func (Notes) Get(ctx *rpc.Context) (string, error) { return "n", nil }

func TestRegister_NoRouterSuffixAllowed(t *testing.T) {
	e := rpc.NewEngine()
	e.Register(&Notes{}) // must not panic

	got := e.Find(rpc.ByTypeName(func(tn string) bool { return tn == "Notes" }))
	if len(got) != 1 || got[0].Name != "Notes" {
		t.Fatalf("Notes not registered under its full name: %+v", got)
	}
}

func TestSelect_NilPredicateMatchesNothing(t *testing.T) {
	e := rpc.NewEngine()
	e.Register(&AlphaRouter{})
	if got := e.Select(nil); got != nil {
		t.Fatalf("nil predicate matched %+v, want nil", got)
	}
}
