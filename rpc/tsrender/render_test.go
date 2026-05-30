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
	got := RenderInline(reflect.TypeOf(S{}))
	for _, want := range cases {
		if !strings.Contains(got, want) {
			t.Errorf("inline missing %q: %s", want, got)
		}
	}
}

func TestRenderInline_OptionalMarkers(t *testing.T) {
	got := RenderInline(reflect.TypeOf(Outer{}))
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
	got := RenderInline(reflect.TypeOf(Outer{}))
	if !strings.Contains(got, "when: string") {
		t.Errorf("time.Time not rendered as string: %s", got)
	}
}

func TestRenderInline_RawMessageIsUnknown(t *testing.T) {
	got := RenderInline(reflect.TypeOf(Outer{}))
	if !strings.Contains(got, "raw?: unknown") {
		t.Errorf("RawMessage not unknown: %s", got)
	}
}

func TestRenderInline_MapIsRecord(t *testing.T) {
	got := RenderInline(reflect.TypeOf(Outer{}))
	if !strings.Contains(got, "Record<string, number>") {
		t.Errorf("map not Record<>: %s", got)
	}
}

func TestRenderDecl_Struct(t *testing.T) {
	got := RenderDecl("Outer", reflect.TypeOf(Outer{}))
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
	got := RenderDecl("Name", reflect.TypeOf("hello"))
	if got != "export type Name = string;" {
		t.Fatalf("got %q", got)
	}
}

func TestCollect_FlattensNamedStructsOnly(t *testing.T) {
	decls := Collect(reflect.TypeOf(Outer{}))
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
