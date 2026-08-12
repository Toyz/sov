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

// SelectAs filters the registry by CAPABILITY: only the router that embeds the
// marker matches, and it comes back typed as the interface + wired to its
// instance. The unexported marker method is invisible to Register (reflect
// lists only exported methods), so AlphaRouter still registers its Ping.
func TestSelectAs_ByInterface(t *testing.T) {
	e := rpc.NewEngine()
	e.Register(&AlphaRouter{})
	e.Register(&BetaRouter{})

	got := rpc.SelectAs[marker](e)
	if len(got) != 1 {
		t.Fatalf("SelectAs matched %d routers, want 1: %+v", len(got), got)
	}
	if got[0].Name != "Alpha" {
		t.Fatalf("matched name = %q, want Alpha", got[0].Name)
	}
	if _, ok := any(got[0].Router).(*AlphaRouter); !ok {
		t.Fatalf("Router is not the live *AlphaRouter: %T", got[0].Router)
	}
}

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

func TestSelect_NilPredicateMatchesNothing(t *testing.T) {
	e := rpc.NewEngine()
	e.Register(&AlphaRouter{})
	if got := e.Select(nil); got != nil {
		t.Fatalf("nil predicate matched %+v, want nil", got)
	}
}
