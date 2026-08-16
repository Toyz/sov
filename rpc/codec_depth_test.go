package rpc

import (
	"strings"
	"testing"
)

func TestJSONDepthOK(t *testing.T) {
	if !jsonDepthOK([]byte(`{"a":[1,2,{"b":3}]}`), 64) {
		t.Fatal("shallow JSON should pass")
	}
	deep := strings.Repeat("[", 100) + strings.Repeat("]", 100)
	if jsonDepthOK([]byte(deep), 64) {
		t.Fatal("100-deep nesting should exceed the cap of 64")
	}
	// Brackets inside a string literal must not count.
	if !jsonDepthOK([]byte(`{"s":"[[[[[[[[[["}`), 3) {
		t.Fatal("brackets inside a string must not count toward depth")
	}
	// Escaped quote inside a string is handled.
	if !jsonDepthOK([]byte(`{"s":"a\"[[[[["}`), 3) {
		t.Fatal("escaped quote in a string must be handled")
	}
	// Exactly at the cap passes; one deeper fails.
	if !jsonDepthOK([]byte(`[[[]]]`), 3) {
		t.Fatal("depth 3 with cap 3 should pass")
	}
	if jsonDepthOK([]byte(`[[[[]]]]`), 3) {
		t.Fatal("depth 4 with cap 3 should fail")
	}
}
