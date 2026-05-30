package rpc

import (
	"reflect"
	"strings"
	"testing"
)

type basicParams struct {
	Handle  string
	Display string
}

type sovTagged struct {
	Name        string   `sov:"name,0,required"`
	OwnerID     string   `sov:"owner_id,1"`
	Tags        []string `sov:"tags,2,omitempty"`
	Description string   `sov:"description,3"`
}

type sovExcluded struct {
	Public   string `sov:"public,0"`
	Internal string `sov:"-"`
}

type sovNamedOnly struct {
	A string `sov:"a"`
	B string `sov:"b"`
}

type jsonFallback struct {
	Foo string `json:"foo"`
	Bar string `json:"bar,omitempty"`
}

func TestFieldMap_BasicSnakeFallback(t *testing.T) {
	fm := mustBuild(t, reflect.TypeOf(basicParams{}))
	if len(fm.Fields) != 2 {
		t.Fatalf("fields = %d", len(fm.Fields))
	}
	if fm.Fields[0].WireName != "handle" || fm.Fields[1].WireName != "display" {
		t.Fatalf("snake fallback wrong: %#v", fm.Fields)
	}
	if fm.Fields[0].Position != 0 || fm.Fields[1].Position != 1 {
		t.Fatalf("auto-positions wrong: %#v", fm.Fields)
	}
	if fm.ByName["handle"] != 0 || fm.ByName["display"] != 1 {
		t.Fatalf("ByName wrong: %#v", fm.ByName)
	}
}

func TestFieldMap_SovTagComplete(t *testing.T) {
	fm := mustBuild(t, reflect.TypeOf(sovTagged{}))
	if len(fm.Fields) != 4 {
		t.Fatalf("fields = %d", len(fm.Fields))
	}
	want := []struct {
		name      string
		pos       int
		required  bool
		omitempty bool
	}{
		{"name", 0, true, false},
		{"owner_id", 1, false, false},
		{"tags", 2, false, true},
		{"description", 3, false, false},
	}
	for i, w := range want {
		f := fm.Fields[i]
		if f.WireName != w.name || f.Position != w.pos || f.Required != w.required || f.Omitempty != w.omitempty {
			t.Errorf("field %d: got %+v, want %+v", i, f, w)
		}
	}
}

func TestFieldMap_Excluded(t *testing.T) {
	fm := mustBuild(t, reflect.TypeOf(sovExcluded{}))
	if len(fm.Fields) != 1 || fm.Fields[0].WireName != "public" {
		t.Fatalf("excluded field leaked: %#v", fm.Fields)
	}
}

func TestFieldMap_NamedOnly(t *testing.T) {
	fm := mustBuild(t, reflect.TypeOf(sovNamedOnly{}))
	for _, f := range fm.Fields {
		if f.Position != -1 {
			t.Fatalf("expected Position=-1 for named-only, got %+v", f)
		}
	}
	if fm.MaxPos != -1 || fm.ByPos != nil {
		t.Fatalf("MaxPos=%d ByPos=%v", fm.MaxPos, fm.ByPos)
	}
}

func TestFieldMap_JSONFallback(t *testing.T) {
	fm := mustBuild(t, reflect.TypeOf(jsonFallback{}))
	if fm.Fields[0].WireName != "foo" || fm.Fields[0].Omitempty {
		t.Fatalf("foo wrong: %+v", fm.Fields[0])
	}
	if fm.Fields[1].WireName != "bar" || !fm.Fields[1].Omitempty {
		t.Fatalf("bar wrong: %+v", fm.Fields[1])
	}
}

type dupName struct {
	A string `sov:"x,0"`
	B string `sov:"x,1"`
}

type dupPos struct {
	A string `sov:"a,0"`
	B string `sov:"b,0"`
}

type requiredOmitempty struct {
	A string `sov:"a,0,required,omitempty"`
}

type unknownOpt struct {
	A string `sov:"a,0,nonsense"`
}

type badIdent struct {
	A string `sov:"BadName,0"`
}

type negativePos struct {
	A string `sov:"a,-1"`
}

type gappedPos struct {
	A string `sov:"a,0"`
	B string `sov:"b,2"`
}

func TestFieldMap_DupName(t *testing.T) {
	expectErr(t, reflect.TypeOf(dupName{}), "duplicate wire name")
}

func TestFieldMap_DupPos(t *testing.T) {
	expectErr(t, reflect.TypeOf(dupPos{}), "duplicate sov tag position")
}

func TestFieldMap_RequiredOmitemptyConflict(t *testing.T) {
	expectErr(t, reflect.TypeOf(requiredOmitempty{}), "'required' and 'omitempty'")
}

func TestFieldMap_UnknownOption(t *testing.T) {
	expectErr(t, reflect.TypeOf(unknownOpt{}), "unknown sov tag option")
}

func TestFieldMap_InvalidIdent(t *testing.T) {
	expectErr(t, reflect.TypeOf(badIdent{}), "not a valid snake_case identifier")
}

func TestFieldMap_NegativePos(t *testing.T) {
	expectErr(t, reflect.TypeOf(negativePos{}), "must be >= 0")
}

func TestFieldMap_PositionGap(t *testing.T) {
	expectErr(t, reflect.TypeOf(gappedPos{}), "must be contiguous")
}

func mustBuild(t *testing.T, rt reflect.Type) *FieldMap {
	t.Helper()
	fm, err := BuildFieldMap(rt)
	if err != nil {
		t.Fatalf("BuildFieldMap(%s): %v", rt, err)
	}
	return fm
}

func expectErr(t *testing.T, rt reflect.Type, substr string) {
	t.Helper()
	_, err := BuildFieldMap(rt)
	if err == nil {
		t.Fatalf("BuildFieldMap(%s): expected error containing %q, got nil", rt, substr)
	}
	if !strings.Contains(err.Error(), substr) {
		t.Fatalf("BuildFieldMap(%s): error %q does not contain %q", rt, err.Error(), substr)
	}
}
