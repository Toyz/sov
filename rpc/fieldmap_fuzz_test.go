package rpc

import (
	"reflect"
	"strconv"
	"testing"
)

// The sov tag grammar parser is a documented bug-hotspot (quoting, escapes,
// commas-in-values, positional slots). These fuzz targets assert the contract
// that matters: no malformed tag may PANIC — the parser must always return a
// structured error instead. A panic here is a boot-time DoS (Register panics on
// a hostile/typo'd tag).

var sovTagSeeds = []string{
	``, `name`, `name,0`, `name,0,required`, `required,omitempty`,
	`desc=hello`, `desc=hello world`, `desc=a, b, c`, `desc=`, `desc==`,
	`'quoted'`, `desc='a, b'`, `desc='unbalanced`, `x'y`, `=leadingeq`,
	`\,`, `\'`, `\\`, `,,,`, `,`, `  `, `name,,`, `name,-1`, `name,999999999999999999999`,
	`header=X-Tenant-Id`, `header=`, `header=X-A,desc=b`, `perm=admin`, `perm=`,
	`title=T,desc=D,doc=Doc,example=E`, `deprecated`, `unknownflag`,
	`a=b=c=d`, `'`, `"`, `\`, `pos,name`, `0,name`,
}

func FuzzSplitSovTokens(f *testing.F) {
	for _, s := range sovTagSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("splitSovTokens panicked on %q: %v", raw, r)
			}
		}()
		// Contract: return tokens or an error, never panic.
		_, _ = splitSovTokens(raw)
	})
}

func FuzzBuildFieldMap(f *testing.F) {
	for _, s := range sovTagSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, tag string) {
		// A struct with one exported string field carrying the fuzzed sov tag.
		// strconv.Quote makes the tag value a valid Go-quoted string, so the
		// StructTag is well-formed and Get("sov") hands the parser the raw
		// fuzzed bytes.
		st := reflect.StructOf([]reflect.StructField{{
			Name: "F",
			Type: reflect.TypeOf(""),
			Tag:  reflect.StructTag(`json:"f" sov:` + strconv.Quote(tag)),
		}})
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("BuildFieldMap panicked on sov=%q: %v", tag, r)
			}
		}()
		// Must not panic on any input; a malformed tag is a returned error.
		_, _ = BuildFieldMap(st)
	})
}
