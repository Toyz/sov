package rpc

import (
	"reflect"
	"strings"
	"testing"
)

// A quoted header name is unquoted before the lookup + reserved-namespace check
// (extractHeaderDirective is a separate pass and must unquote too).
func TestSovTag_QuotedHeaderNameUnquoted(t *testing.T) {
	type p struct {
		X string `sov:"header='X-Tenant-Id'"`
	}
	fm, err := BuildFieldMap(reflect.TypeOf(p{}))
	if err != nil {
		t.Fatalf("BuildFieldMap: %v", err)
	}
	if got := fm.Fields[fm.HeaderFields[0]].HeaderSource; got != "X-Tenant-Id" {
		t.Fatalf("HeaderSource = %q, want X-Tenant-Id (unquoted)", got)
	}
}

// A quoted X-Sov-* header name is still rejected — the reserved check runs after
// unquoting, so quoting can't sneak past it.
func TestSovTag_QuotedXSovHeaderRejected(t *testing.T) {
	type p struct {
		X string `sov:"header='X-Sov-Subject'"`
	}
	if _, err := BuildFieldMap(reflect.TypeOf(p{})); err == nil || !strings.Contains(err.Error(), "X-Sov-") {
		t.Fatalf("quoted X-Sov header must still be rejected, got %v", err)
	}
}

// perm= on the blank sentinel is unquoted too, so a comma-bearing perm token is
// not corrupted with literal quotes.
func TestSovTag_QuotedPermUnquoted(t *testing.T) {
	type p struct {
		_ struct{} `sov:"perm='role:admin,role:owner'"`
		X string   `json:"x"`
	}
	fm, err := BuildFieldMap(reflect.TypeOf(p{}))
	if err != nil {
		t.Fatalf("BuildFieldMap: %v", err)
	}
	if fm.Perm != "role:admin,role:owner" {
		t.Fatalf("Perm = %q, want role:admin,role:owner (unquoted)", fm.Perm)
	}
}

// A quoted value opened after '=' but never closed is a LOUD build error.
func TestSovTag_UnbalancedQuoteIsBuildError(t *testing.T) {
	type p struct {
		X string `sov:"x,0,desc='oops"`
	}
	if _, err := BuildFieldMap(reflect.TypeOf(p{})); err == nil || !strings.Contains(err.Error(), "unbalanced") {
		t.Fatalf("unbalanced quote must be a build error, got %v", err)
	}
}

// A possessive/contraction apostrophe is a LITERAL character (a quote opens
// only at a value start, right after '='), so an apostrophe in one directive
// never balances one in another and never swallows a flag. This is the exact
// bug the whole-tag quote-balance approach had: desc=User's Id,required must
// keep `required` and both desc/title.
func TestSovTag_ApostropheDoesNotSwallowFlags(t *testing.T) {
	type p struct {
		F string `sov:"f,0,desc=User's Id,required,title=Value's Name"`
	}
	fm, err := BuildFieldMap(reflect.TypeOf(p{}))
	if err != nil {
		t.Fatalf("BuildFieldMap: %v", err)
	}
	f := fm.Fields[0]
	if !f.Required {
		t.Fatalf("required silently dropped by a possessive apostrophe: %+v", f)
	}
	if f.Desc != "User's Id" {
		t.Fatalf("Desc = %q, want \"User's Id\"", f.Desc)
	}
	if f.Title != "Value's Name" {
		t.Fatalf("Title = %q, want \"Value's Name\"", f.Title)
	}
}

// A backslash-escaped apostrophe embeds a literal apostrophe (no wrapping quotes
// needed), and composes with a quoted comma-bearing value.
func TestSovTag_EscapedApostrophe(t *testing.T) {
	type p struct {
		A string `sov:"a,0,desc=isn\\'t"`
		B string `sov:"b,1,desc='a, isn\\'t b'"`
	}
	fm, err := BuildFieldMap(reflect.TypeOf(p{}))
	if err != nil {
		t.Fatalf("BuildFieldMap: %v", err)
	}
	if fm.Fields[0].Desc != "isn't" {
		t.Fatalf("A.Desc = %q, want isn't", fm.Fields[0].Desc)
	}
	if fm.Fields[1].Desc != "a, isn't b" {
		t.Fatalf("B.Desc = %q, want \"a, isn't b\"", fm.Fields[1].Desc)
	}
}

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
