package rpc

import (
	"reflect"
	"strings"
	"testing"
)

type kvFields struct {
	Handle string `sov:"handle,0,required,title=Username,desc=Unique handle,example=alice"`
	Note   string `sov:"note,1,doc=Long-form note shown as tooltip"`
}

type kvCommaInValue struct {
	Slug string `sov:"slug,0,title=Hello\\, world,example=foo\\,bar"`
}

type kvUnknownKey struct {
	X string `sov:"x,0,nope=value"`
}

type kvEmptyValue struct {
	X string `sov:"x,0,title="`
}

type kvDupKey struct {
	X string `sov:"x,0,title=A,title=B"`
}

func TestFieldMap_KVBasic(t *testing.T) {
	fm := mustBuild(t, reflect.TypeOf(kvFields{}))
	if fm.Fields[0].Title != "Username" {
		t.Errorf("title got %q", fm.Fields[0].Title)
	}
	if fm.Fields[0].Desc != "Unique handle" {
		t.Errorf("desc got %q", fm.Fields[0].Desc)
	}
	if fm.Fields[0].Example != "alice" {
		t.Errorf("example got %q", fm.Fields[0].Example)
	}
	if fm.Fields[1].Doc != "Long-form note shown as tooltip" {
		t.Errorf("doc got %q", fm.Fields[1].Doc)
	}
}

func TestFieldMap_KVEscapedComma(t *testing.T) {
	fm := mustBuild(t, reflect.TypeOf(kvCommaInValue{}))
	if fm.Fields[0].Title != "Hello, world" {
		t.Errorf("title got %q", fm.Fields[0].Title)
	}
	if fm.Fields[0].Example != "foo,bar" {
		t.Errorf("example got %q", fm.Fields[0].Example)
	}
}

func TestFieldMap_KVUnknownKey(t *testing.T) {
	_, err := BuildFieldMap(reflect.TypeOf(kvUnknownKey{}))
	if err == nil || !strings.Contains(err.Error(), `unknown sov tag key "nope"`) {
		t.Fatalf("expected unknown-key error, got %v", err)
	}
}

func TestFieldMap_KVEmptyValue(t *testing.T) {
	_, err := BuildFieldMap(reflect.TypeOf(kvEmptyValue{}))
	if err == nil || !strings.Contains(err.Error(), "empty value") {
		t.Fatalf("expected empty-value error, got %v", err)
	}
}

func TestFieldMap_KVDupKey(t *testing.T) {
	_, err := BuildFieldMap(reflect.TypeOf(kvDupKey{}))
	if err == nil || !strings.Contains(err.Error(), `duplicate sov tag key "title"`) {
		t.Fatalf("expected duplicate-key error, got %v", err)
	}
}
