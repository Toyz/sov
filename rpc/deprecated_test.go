package rpc

import (
	"reflect"
	"testing"
)

type depWithReason struct {
	_    struct{} `sov:"deprecated=use New.thing instead"`
	Name string   `json:"name"`
}

type depBare struct {
	_    struct{} `sov:"deprecated"`
	Name string   `json:"name"`
}

type depWithPerm struct {
	_    struct{} `sov:"perm=admin,deprecated=gone soon"`
	Name string   `json:"name"`
}

func TestSentinel_DeprecatedWithReason(t *testing.T) {
	fm, err := BuildFieldMap(reflect.TypeOf(depWithReason{}))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !fm.Deprecated {
		t.Fatal("Deprecated should be set")
	}
	if fm.DeprecatedReason != "use New.thing instead" {
		t.Fatalf("reason = %q", fm.DeprecatedReason)
	}
}

func TestSentinel_DeprecatedBare(t *testing.T) {
	fm, err := BuildFieldMap(reflect.TypeOf(depBare{}))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !fm.Deprecated || fm.DeprecatedReason != "" {
		t.Fatalf("bare: deprecated=%v reason=%q", fm.Deprecated, fm.DeprecatedReason)
	}
}

// deprecated coexists with perm on the same sentinel.
func TestSentinel_DeprecatedWithPerm(t *testing.T) {
	fm, err := BuildFieldMap(reflect.TypeOf(depWithPerm{}))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if fm.Perm != "admin" {
		t.Fatalf("perm = %q", fm.Perm)
	}
	if !fm.Deprecated || fm.DeprecatedReason != "gone soon" {
		t.Fatalf("deprecated=%v reason=%q", fm.Deprecated, fm.DeprecatedReason)
	}
}
