package sov

import (
	"strings"
	"testing"
)

func TestParseValue(t *testing.T) {
	cases := []struct{ in, want string }{
		{``, ``},
		{`bare`, `bare`},
		{`  spaced  `, `spaced`},
		{`"hello world"`, `hello world`},
		{`'single'`, `single`},
		{`""`, ``},
		{`bare # comment`, `bare`},
		{"tok\t# tab comment", `tok`},
		{`"secret" # prod key`, `secret`},     // quoted + trailing comment
		{`'p@ss word' # the pw`, `p@ss word`}, // quoted + space + comment
		{`"a # b" # trailing`, `a # b`},       // # inside quotes preserved
		{`#FF0000`, `#FF0000`},                // hex color, leading # kept
		{`0xDE#AD`, `0xDE#AD`},                // # mid-token kept (no ws before)
		{`eyJabc.def#ghi`, `eyJabc.def#ghi`},  // tight token w/ fragment
		{`"unterminated`, `"unterminated`},    // no closing quote: literal
		{`val=with=eq`, `val=with=eq`},        // '=' in value untouched here
		{`a  #  c`, `a`},                      // multiple spaces around #
	}
	for _, c := range cases {
		if got := parseValue(c.in); got != c.want {
			t.Errorf("parseValue(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// FuzzParseValue asserts invariants rather than exact output: parseValue
// must never panic, never grow the input, and a cleanly double-quoted
// string (no inner quote/newline) must round-trip to its content even when
// a trailing comment follows the closing quote.
func FuzzParseValue(f *testing.F) {
	for _, s := range []string{``, `x`, `a b`, ` x `, `#fff`, `0x#1`, `p@ss!`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got := parseValue(s) // must not panic
		if len(got) > len(s) {
			t.Errorf("parseValue(%q) = %q grew the input", s, got)
		}
		// Clean double-quoted round-trip: `"X"` and `"X" # c` -> X when X
		// has no double-quote or newline.
		if !strings.ContainsAny(s, "\"\n") {
			if out := parseValue(`"` + s + `"`); out != s {
				t.Errorf(`round-trip parseValue("%s") = %q, want %q`, s, out, s)
			}
			if out := parseValue(`"` + s + `" # comment`); out != s {
				t.Errorf(`round-trip+comment parseValue("%s" # comment) = %q, want %q`, s, out, s)
			}
		}
	})
}
