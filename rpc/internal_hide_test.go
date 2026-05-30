package rpc

import (
	"reflect"
	"testing"
)

type HideMarkerRouter struct{}

func (r *HideMarkerRouter) Open(ctx *Context) error     { return nil }
func (r *HideMarkerRouter) Soft(ctx *Context) error     { return nil }
func (r *HideMarkerRouter) Hard(ctx *Context) error     { return nil }
func (r *HideMarkerRouter) HiddenMethods() []string     { return []string{"soft"} }
func (r *HideMarkerRouter) HardHiddenMethods() []string { return []string{"hard"} }

type tagSoftParams struct {
	_  struct{} `sov:"internal"`
	ID string   `json:"id"`
}
type tagHardParams struct {
	_  struct{} `sov:"internal,hard"`
	ID string   `json:"id"`
}

// NamedInternalParams has a real exported field literally named
// "internal" — it must stay a normal wire field, not flip the method.
type NamedInternalParams struct {
	Internal string `json:"internal"`
}

type HideTagRouter struct{}

func (r *HideTagRouter) Plain(ctx *Context) error                         { return nil }
func (r *HideTagRouter) Peek(ctx *Context, p *tagSoftParams) error        { return nil }
func (r *HideTagRouter) Vault(ctx *Context, p *tagHardParams) error       { return nil }
func (r *HideTagRouter) Named(ctx *Context, p *NamedInternalParams) error { return nil }

func descOf(t *testing.T, e *Engine, router, method string) MethodDescriptor {
	t.Helper()
	for _, rd := range e.Describe() {
		if rd.Router != router {
			continue
		}
		for _, md := range rd.Methods {
			if md.Method == method {
				return md
			}
		}
	}
	t.Fatalf("method %s.%s not found", router, method)
	return MethodDescriptor{}
}

func TestDescribe_HideMarkers(t *testing.T) {
	e := NewEngine()
	e.Register(&HideMarkerRouter{})

	if md := descOf(t, e, "HideMarker", "open"); md.Internal || md.HardHidden {
		t.Errorf("open: Internal=%v HardHidden=%v, want both false", md.Internal, md.HardHidden)
	}
	if md := descOf(t, e, "HideMarker", "soft"); !md.Internal || md.HardHidden {
		t.Errorf("soft: Internal=%v HardHidden=%v, want Internal only", md.Internal, md.HardHidden)
	}
	if md := descOf(t, e, "HideMarker", "hard"); md.Internal || !md.HardHidden {
		t.Errorf("hard: Internal=%v HardHidden=%v, want HardHidden only", md.Internal, md.HardHidden)
	}
}

func TestDescribe_HideTagSentinel(t *testing.T) {
	e := NewEngine()
	e.Register(&HideTagRouter{})

	if md := descOf(t, e, "HideTag", "plain"); md.Internal || md.HardHidden {
		t.Errorf("plain: Internal=%v HardHidden=%v, want both false", md.Internal, md.HardHidden)
	}
	if md := descOf(t, e, "HideTag", "peek"); !md.Internal || md.HardHidden {
		t.Errorf(`peek (sov:"internal"): Internal=%v HardHidden=%v, want Internal only`, md.Internal, md.HardHidden)
	}
	if md := descOf(t, e, "HideTag", "vault"); md.Internal || !md.HardHidden {
		t.Errorf(`vault (sov:"internal,hard"): Internal=%v HardHidden=%v, want HardHidden only`, md.Internal, md.HardHidden)
	}
	// A real field named "internal" must NOT hide the method, and must
	// remain a wire field.
	md := descOf(t, e, "HideTag", "named")
	if md.Internal || md.HardHidden {
		t.Errorf("named: Internal=%v HardHidden=%v, want both false (named field, not a sentinel)", md.Internal, md.HardHidden)
	}
	found := false
	for _, f := range md.Params {
		if f.JSONName == "internal" {
			found = true
		}
	}
	if !found {
		t.Error(`named: wire field "internal" missing — a real field named internal must survive`)
	}
}

func TestBuildFieldMap_BadSentinel(t *testing.T) {
	type badParams struct {
		_ struct{} `sov:"internal,bogus"`
	}
	if _, err := BuildFieldMap(reflect.TypeOf(badParams{})); err == nil {
		t.Error("expected error for unknown sentinel directive 'bogus'")
	}
	type notInternal struct {
		_ struct{} `sov:"hard"`
	}
	if _, err := BuildFieldMap(reflect.TypeOf(notInternal{})); err == nil {
		t.Error("expected error: sentinel must start with 'internal'")
	}
}
