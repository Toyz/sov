package tsrender

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

type Inner struct {
	A string `json:"a"`
	B int    `json:"b,omitempty"`
}

type Outer struct {
	ID     string          `json:"id"`
	Inner  *Inner          `json:"inner,omitempty"`
	Tags   []string        `json:"tags,omitempty"`
	Lookup map[string]int  `json:"lookup,omitempty"`
	When   time.Time       `json:"when"`
	Raw    json.RawMessage `json:"raw,omitempty"`
}

func TestRenderInline_Primitives(t *testing.T) {
	cases := map[reflect.Kind]string{
		reflect.String: "string",
		reflect.Bool:   "boolean",
		reflect.Int:    "number",
		reflect.Int64:  "number",
	}
	type S struct {
		StrField   string
		BoolField  bool
		IntField   int
		Int64Field int64
	}
	got := RenderInline(reflect.TypeFor[S]())
	for _, want := range cases {
		if !strings.Contains(got, want) {
			t.Errorf("inline missing %q: %s", want, got)
		}
	}
}

func TestRenderInline_OptionalMarkers(t *testing.T) {
	got := RenderInline(reflect.TypeFor[Outer]())
	if !strings.Contains(got, "id: string") {
		t.Errorf("required field lacks marker: %s", got)
	}
	if !strings.Contains(got, "inner?: ") {
		t.Errorf("pointer field not optional: %s", got)
	}
	if !strings.Contains(got, "tags?: string[]") {
		t.Errorf("omitempty slice not optional: %s", got)
	}
}

func TestRenderInline_TimeIsString(t *testing.T) {
	got := RenderInline(reflect.TypeFor[Outer]())
	if !strings.Contains(got, "when: string") {
		t.Errorf("time.Time not rendered as string: %s", got)
	}
}

func TestRenderInline_RawMessageIsUnknown(t *testing.T) {
	got := RenderInline(reflect.TypeFor[Outer]())
	if !strings.Contains(got, "raw?: unknown") {
		t.Errorf("RawMessage not unknown: %s", got)
	}
}

func TestRenderInline_MapIsRecord(t *testing.T) {
	got := RenderInline(reflect.TypeFor[Outer]())
	if !strings.Contains(got, "Record<string, number>") {
		t.Errorf("map not Record<>: %s", got)
	}
}

func TestRenderDecl_Struct(t *testing.T) {
	got := RenderDecl("Outer", reflect.TypeFor[Outer]())
	for _, want := range []string{
		"export interface Outer {",
		"  id: string;",
		"  inner?: ",
		"  tags?: string[];",
		"  when: string;",
		"}",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("decl missing %q:\n%s", want, got)
		}
	}
}

func TestRenderDecl_NonStruct(t *testing.T) {
	got := RenderDecl("Name", reflect.TypeFor[string]())
	if got != "export type Name = string;" {
		t.Fatalf("got %q", got)
	}
}

func TestCollect_FlattensNamedStructsOnly(t *testing.T) {
	decls := Collect(reflect.TypeFor[Outer]())
	names := make([]string, 0, len(decls))
	for _, d := range decls {
		names = append(names, d.Name)
	}
	// Outer + Inner — both named. RawMessage is named but renders as
	// unknown and isn't a struct walked into here.
	if len(names) != 2 || names[0] != "Inner" || names[1] != "Outer" {
		t.Fatalf("got %v", names)
	}
}

// SelfNode is directly self-referential via a slice-of-pointer — the exact
// shape (mininote's SharedNode.ViewMatches []*SharedNode) that overflowed the
// stack before the renderer grew a cycle guard.
type SelfNode struct {
	ID       string      `json:"id"`
	Children []*SelfNode `json:"children,omitempty"`
}

// NodeA <-> NodeB are mutually recursive.
type NodeA struct {
	B *NodeB `json:"b,omitempty"`
}
type NodeB struct {
	A *NodeA `json:"a,omitempty"`
}

func TestRenderInline_SelfReferential_Terminates(t *testing.T) {
	// Before the fix this overflowed the stack. It must terminate and break the
	// cycle to `unknown` (can't inline an infinite type), while still rendering
	// the non-recursive fields.
	got := RenderInline(reflect.TypeFor[SelfNode]())
	if !strings.Contains(got, "id: string") {
		t.Errorf("expected id field: %s", got)
	}
	if !strings.Contains(got, "children?: unknown[]") {
		t.Errorf("recursive field should break to unknown[]: %s", got)
	}
}

func TestRenderDecl_SelfReferential_Terminates(t *testing.T) {
	got := RenderDecl("SelfNode", reflect.TypeFor[SelfNode]())
	if !strings.HasPrefix(got, "export interface SelfNode {") {
		t.Errorf("bad decl: %s", got)
	}
	// The self-reference resolves to unknown[] rather than re-expanding the type
	// inside its own interface.
	if !strings.Contains(got, "children?: unknown[]") {
		t.Errorf("self-ref field should be unknown[]: %s", got)
	}
}

func TestCollect_SelfReferential_Terminates(t *testing.T) {
	// Collect walked a named struct's fields even when already seen — infinite on
	// a cycle. It must now emit exactly one decl and terminate.
	decls := Collect(reflect.TypeFor[SelfNode]())
	if len(decls) != 1 || decls[0].Name != "SelfNode" {
		t.Fatalf("expected one SelfNode decl, got %v", decls)
	}
}

func TestRender_MutualRecursion_Terminates(t *testing.T) {
	// A -> B -> A must terminate for both the inline and collect paths.
	got := RenderInline(reflect.TypeFor[NodeA]())
	if !strings.Contains(got, "unknown") {
		t.Errorf("mutual recursion should break to unknown: %s", got)
	}
	decls := Collect(reflect.TypeFor[NodeA]())
	if len(decls) != 2 {
		t.Fatalf("expected NodeA + NodeB decls, got %v", decls)
	}
}

func TestRenderInline_NonCyclicReuseStillExpands(t *testing.T) {
	// The guard is PATH-scoped, not global: a type reused across sibling fields
	// (a DAG, not a cycle) must still render its full shape each time.
	type Leaf struct {
		V string `json:"v"`
	}
	type Tree struct {
		Left  Leaf `json:"left"`
		Right Leaf `json:"right"`
	}
	got := RenderInline(reflect.TypeFor[Tree]())
	if strings.Count(got, "v: string") != 2 {
		t.Errorf("both DAG branches should expand: %s", got)
	}
}
