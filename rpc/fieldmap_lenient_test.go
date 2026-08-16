package rpc

import (
	"reflect"
	"testing"
	"time"
)

// The position slot in a sov tag is optional: a non-integer token right after
// the name is a flag/kv, not a position. Being strict here was a footgun that
// rejected natural forms like `name,required` and even sov's own PageParams.
func TestBuildFieldMap_PositionOptional(t *testing.T) {
	type P struct {
		A string `sov:"a,required,title=Ay"` // name + flags, NO position
		B string `sov:"b,title=Bee"`         // name + kv, no position
		C string `sov:"c,0,required"`        // explicit position still works
		D string `sov:"d,,desc=dee"`         // explicit empty position + flag
	}
	fm, err := BuildFieldMap(reflect.TypeOf(P{}))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	by := map[string]FieldInfo{}
	for _, f := range fm.Fields {
		by[f.WireName] = f
	}
	if !by["a"].Required || by["a"].Position != -1 {
		t.Errorf("a: required=%v position=%d, want required=true position=-1", by["a"].Required, by["a"].Position)
	}
	if by["b"].Position != -1 {
		t.Errorf("b position = %d, want -1", by["b"].Position)
	}
	if by["c"].Position != 0 || !by["c"].Required {
		t.Errorf("c: position=%d required=%v, want 0/true", by["c"].Position, by["c"].Required)
	}
	if by["d"].Position != -1 {
		t.Errorf("d (empty position slot) position = %d, want -1", by["d"].Position)
	}
}

// rpc.PageParams must be usable as a method param — its tags use the
// name-then-kv form the fix above enables. Regression for a panic at Register.
func TestBuildFieldMap_PageParamsIsValid(t *testing.T) {
	if _, err := BuildFieldMap(reflect.TypeOf(PageParams{})); err != nil {
		t.Fatalf("PageParams must build a valid field map: %v", err)
	}
}

// package "main" is the user's entrypoint, not stdlib — its types must not be
// dropped from the type catalog. (time.Time is stdlib; a local type is not.)
func TestIsStdlibType(t *testing.T) {
	if !isStdlibType(reflect.TypeOf(time.Time{})) {
		t.Error("time.Time should be classified stdlib")
	}
	if isStdlibType(reflect.TypeOf(PageParams{})) {
		t.Error("a sov type must not be classified stdlib")
	}
}
