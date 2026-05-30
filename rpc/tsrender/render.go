// Package tsrender turns Go reflect types into TypeScript-shaped
// strings. Two output shapes:
//
//   - Inline: `{ field?: string; other: number[] }` — one-liner, what
//     the Explorer UI renders next to each method (and what
//     rpc.TSPreviewForMethod consumed for years before this split).
//   - Decl:   `export interface Name {\n  field?: string;\n}` — full
//     `.d.ts` form, what the sovgen CLI emits per named type.
//   - Collect: walk a root type, emit one TypeDecl per named struct
//     reached (including nested). Powers the sovgen "one interface
//     per type" file layout.
//
// Honors the same conventions tspreview did: `json:"name,omitempty"`
// drives wire name + optional marker; pointer types are optional;
// json.RawMessage decays to `unknown`. Sov tag enrichment (title,
// desc, doc, example) is NOT consumed here — those live on
// ParamField/FieldInfo and the consumer (Explorer, sovgen) decides how
// to render them around the inline/decl shapes this package emits.
package tsrender

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// RenderInline returns a one-line TypeScript representation of t.
// Nested structs are expanded inline (anonymous), not referenced by
// name — suitable for previews and the Explorer's hint column.
func RenderInline(t reflect.Type) string { return render(t) }

// RenderDecl returns `export interface <name> { ...fields... }` form.
// Use for top-level `.d.ts` emission. If t is not a struct, returns
// `export type <name> = <inline>;`.
func RenderDecl(name string, t reflect.Type) string {
	if t == nil {
		return fmt.Sprintf("export type %s = unknown;", name)
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return fmt.Sprintf("export type %s = %s;", name, render(t))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "export interface %s {\n", name)
	for _, f := range structFields(t) {
		marker := ""
		if f.Optional {
			marker = "?"
		}
		fmt.Fprintf(&b, "  %s%s: %s;\n", f.Name, marker, render(f.Type))
	}
	b.WriteString("}")
	return b.String()
}

// TypeDecl is the unit of output from Collect — a named type plus the
// rendered declaration.
type TypeDecl struct {
	Name string
	Decl string
}

// Collect walks t recursively and returns one TypeDecl per named
// struct encountered (including t itself if named). Used by the
// codegen CLI to flatten an aggregated catalog into a deterministic
// list of `export interface` declarations.
//
// The returned slice is sorted by Name for reproducible output.
// Anonymous structs are inlined into their parent and do not appear
// here.
func Collect(t reflect.Type) []TypeDecl {
	seen := map[string]bool{}
	var out []TypeDecl
	collect(t, seen, &out)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func collect(t reflect.Type, seen map[string]bool, out *[]TypeDecl) {
	if t == nil {
		return
	}
	switch t.Kind() {
	case reflect.Ptr, reflect.Slice, reflect.Array:
		collect(t.Elem(), seen, out)
		return
	case reflect.Map:
		collect(t.Key(), seen, out)
		collect(t.Elem(), seen, out)
		return
	case reflect.Struct:
		// Skip stdlib types that render() flattens to a primitive — they
		// have no useful interface form on the wire.
		if t.PkgPath() == "time" && t.Name() == "Time" {
			return
		}
		if name := t.Name(); name != "" && !seen[name] {
			seen[name] = true
			*out = append(*out, TypeDecl{Name: name, Decl: RenderDecl(name, t)})
		}
		for i := 0; i < t.NumField(); i++ {
			sf := t.Field(i)
			if !sf.IsExported() {
				continue
			}
			collect(sf.Type, seen, out)
		}
	}
}

// fieldInfo is the subset of struct-field metadata the renderer needs.
// Kept local so this package doesn't import rpc (which avoids a cycle
// with rpc/tspreview.go shim).
type fieldInfo struct {
	Name     string
	Type     reflect.Type
	Optional bool
}

func structFields(t reflect.Type) []fieldInfo {
	out := make([]fieldInfo, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		tag := sf.Tag.Get("json")
		if tag == "-" {
			continue
		}
		segments := strings.Split(tag, ",")
		name := segments[0]
		if name == "" {
			name = sf.Name
		}
		optional := strings.Contains(tag, "omitempty") || sf.Type.Kind() == reflect.Ptr
		out = append(out, fieldInfo{Name: name, Type: sf.Type, Optional: optional})
	}
	return out
}

func render(t reflect.Type) string {
	if t == nil {
		return "unknown"
	}
	switch t.Kind() {
	case reflect.Ptr:
		return render(t.Elem())
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Slice, reflect.Array:
		if isJSONRawMessage(t) {
			return "unknown"
		}
		return render(t.Elem()) + "[]"
	case reflect.Map:
		return fmt.Sprintf("Record<%s, %s>", render(t.Key()), render(t.Elem()))
	case reflect.Interface:
		return "unknown"
	case reflect.Struct:
		// time.Time → ISO string (matches JSON marshaling default).
		if t.PkgPath() == "time" && t.Name() == "Time" {
			return "string"
		}
		if t.NumField() == 0 {
			return "{}"
		}
		fields := structFields(t)
		parts := make([]string, 0, len(fields))
		for _, f := range fields {
			marker := ""
			if f.Optional {
				marker = "?"
			}
			parts = append(parts, fmt.Sprintf("%s%s: %s", f.Name, marker, render(f.Type)))
		}
		return "{ " + strings.Join(parts, "; ") + " }"
	default:
		return "unknown"
	}
}

// isJSONRawMessage mirrors rpc.isJSONRawMessage. It is duplicated (not
// shared) on purpose: rpc imports rpc/tsrender (see rpc/tspreview.go), so
// tsrender importing rpc back would be a cycle. The predicate is trivial
// and stable; a shared leaf package for seven lines isn't worth the
// indirection.
func isJSONRawMessage(t reflect.Type) bool {
	if t == nil {
		return false
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8 && t.Name() == "RawMessage"
}
