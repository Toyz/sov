package rpc

import (
	"reflect"
	"testing"
)

func TestValidateConstraints_MaxLen(t *testing.T) {
	type P struct {
		Name string   `json:"name" sov:"name,0,maxlen=5"`
		Tags []string `json:"tags" sov:"tags,1,maxlen=2"`
	}
	fm, err := BuildFieldMap(reflect.TypeOf(P{}))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if e := validateConstraints(reflect.ValueOf(&P{Name: "abc", Tags: []string{"a"}}).Elem(), fm); e != nil {
		t.Fatalf("within limits should pass: %v", e)
	}
	e := validateConstraints(reflect.ValueOf(&P{Name: "toolong", Tags: []string{"a", "b", "c"}}).Elem(), fm)
	if e == nil {
		t.Fatal("over limits should fail")
	}
	if len(e.Details) != 2 {
		t.Fatalf("want 2 field details (name + tags), got %d: %+v", len(e.Details), e.Details)
	}
	if e.Status != 400 {
		t.Fatalf("status = %d", e.Status)
	}
}

func TestBuildFieldMap_RejectsBadMaxLen(t *testing.T) {
	type P struct {
		Name string `sov:"name,0,maxlen=nope"`
	}
	if _, err := BuildFieldMap(reflect.TypeOf(P{})); err == nil {
		t.Fatal("maxlen=nope must error at parse")
	}
}
