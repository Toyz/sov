package rpc

import (
	"reflect"
	"testing"
)

// A single-quoted kv value may contain commas and spaces — the ergonomic way to
// write human text (desc=/title=/doc=/example=) that contains a comma.
func TestSovTag_QuotedValueWithCommas(t *testing.T) {
	type p struct {
		X string `sov:"x,0,desc='this, is my desc',title='A, B, C'"`
	}
	fm, err := BuildFieldMap(reflect.TypeOf(p{}))
	if err != nil {
		t.Fatalf("BuildFieldMap: %v", err)
	}
	f := fm.Fields[0]
	if f.Desc != "this, is my desc" {
		t.Fatalf("Desc = %q, want \"this, is my desc\"", f.Desc)
	}
	if f.Title != "A, B, C" {
		t.Fatalf("Title = %q, want \"A, B, C\"", f.Title)
	}
}

// A quoted value with no comma is unwrapped to its plain content.
func TestSovTag_QuotedValueNoComma(t *testing.T) {
	type p struct {
		X string `sov:"x,0,title='Username'"`
	}
	fm, err := BuildFieldMap(reflect.TypeOf(p{}))
	if err != nil {
		t.Fatalf("BuildFieldMap: %v", err)
	}
	if fm.Fields[0].Title != "Username" {
		t.Fatalf("Title = %q, want Username", fm.Fields[0].Title)
	}
}

// The backslash-escape for a literal comma still works (back-compat). The tag
// source uses \\, which StructTag.Get unwraps to \, that splitSovTokens reads
// as an escaped comma.
func TestSovTag_EscapedCommaStillWorks(t *testing.T) {
	type p struct {
		X string `sov:"x,0,desc=a\\,b"`
	}
	fm, err := BuildFieldMap(reflect.TypeOf(p{}))
	if err != nil {
		t.Fatalf("BuildFieldMap: %v", err)
	}
	if fm.Fields[0].Desc != "a,b" {
		t.Fatalf("Desc = %q, want a,b", fm.Fields[0].Desc)
	}
}

// A bare unquoted comma still splits (it can't disambiguate from the separator)
// — so it's a loud build error telling the author to quote or escape, never a
// silent mangle.
func TestSovTag_UnquotedCommaIsBuildError(t *testing.T) {
	type p struct {
		X string `sov:"x,0,desc=a, b"`
	}
	if _, err := BuildFieldMap(reflect.TypeOf(p{})); err == nil {
		t.Fatalf("unquoted comma in a kv value should be a build error (quote or escape it)")
	}
}
