package rpc

import (
	"encoding/json"
	"reflect"
)

// isJSONRawMessage reports whether t is encoding/json.RawMessage (wire:
// arbitrary JSON value, not a byte slice).
func isJSONRawMessage(t reflect.Type) bool {
	if t == nil {
		return false
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t == reflect.TypeFor[json.RawMessage]()
}

// openAPITypeString collapses a Go type into the OpenAPI-ish surface a
// designer or codegen cares about: string, number, boolean, array,
// object. Not a full schema — a hint.
func openAPITypeString(t reflect.Type) string {
	if t == nil {
		return "object"
	}
	switch t.Kind() {
	case reflect.Ptr:
		return openAPITypeString(t.Elem())
	case reflect.Slice, reflect.Array:
		if isJSONRawMessage(t) {
			return "object"
		}
		return "array"
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Struct, reflect.Map:
		// Stdlib struct types that JSON-marshal to a primitive land
		// here. Hard-code the known cases so the catalog reflects
		// the actual wire shape (time.Time → RFC3339 string).
		if scalar := stdlibJSONScalar(t); scalar != "" {
			return scalar
		}
		return "object"
	default:
		return "string"
	}
}

// stdlibJSONScalar returns the OpenAPI scalar type a stdlib struct
// marshals to on the JSON wire, or "" when t isn't a known stdlib
// scalar struct. Catalog + codegen rely on this so e.g. Claims.Exp
// (a time.Time) surfaces as `string`, not as a dangling *Time
// reference (Time itself is filtered from the catalog as stdlib).
func stdlibJSONScalar(t reflect.Type) string {
	if t == nil {
		return ""
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch t.PkgPath() + "." + t.Name() {
	case "time.Time":
		return "string"
	}
	return ""
}

// designerTypeLabel turns an OpenAPI type into a short human label.
func designerTypeLabel(openAPIType string) string {
	switch openAPIType {
	case "string":
		return "Text"
	case "number", "integer":
		return "Number"
	case "boolean":
		return "On / off"
	case "array":
		return "List"
	case "object":
		return "JSON object"
	default:
		return openAPIType
	}
}

// schemaLine is a one-line hint for arrays ("List of text values" etc.).
func schemaLine(t reflect.Type) string {
	if t == nil {
		return ""
	}
	if t.Kind() == reflect.Ptr {
		return schemaLine(t.Elem())
	}
	if t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		if isJSONRawMessage(t) {
			return "JSON document"
		}
		switch openAPITypeString(t.Elem()) {
		case "string":
			return "List of text values"
		case "number":
			return "List of numbers"
		case "boolean":
			return "List of yes / no values"
		default:
			return "List of values"
		}
	}
	return designerTypeLabel(openAPITypeString(t))
}

// describeFieldMap emits ParamField records from a resolved FieldMap.
// Source of truth: BuildFieldMap output. Used by Engine.Describe so
// introspection and dispatch never disagree on what a field is named or
// whether it is required.
func describeFieldMap(fm *FieldMap) []ParamField {
	if fm == nil {
		return nil
	}
	out := make([]ParamField, 0, len(fm.Fields))
	for _, f := range fm.Fields {
		st := openAPITypeString(f.Type)
		hint := designerTypeLabel(st)
		if st == "array" {
			hint = schemaLine(f.Type)
		}
		pf := ParamField{
			JSONName:     f.WireName,
			SchemaType:   st,
			DesignerHint: hint,
			Required:     f.Required,
			Position:     f.Position,
			Omitempty:    f.Omitempty,
			Deprecated:   f.Deprecated,
			Title:        f.Title,
			Desc:         f.Desc,
			Doc:          f.Doc,
			Example:      f.Example,
		}
		if st == "object" {
			pf.TypeName = nestedTypeName(f.Type)
		} else if st == "array" {
			pf.TypeName = elemTypeName(f.Type)
		}
		// Stdlib types stay out of the catalog — clear TypeName so
		// codegen falls back to the scalar type instead of emitting a
		// dangling reference to e.g. *Time.
		if pf.TypeName != "" {
			ft := f.Type
			for ft.Kind() == reflect.Ptr {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Slice || ft.Kind() == reflect.Array {
				ft = ft.Elem()
				for ft.Kind() == reflect.Ptr {
					ft = ft.Elem()
				}
			}
			if isStdlibType(ft) {
				pf.TypeName = ""
			}
		}
		out = append(out, pf)
	}
	return out
}

// describeStructFields keeps the old `json:`-only path alive for
// callers (such as nested type catalog construction) that don't have a
// FieldMap. Prefer describeFieldMap on registered params.
func describeStructFields(t reflect.Type) []ParamField {
	fm, err := BuildFieldMap(t)
	if err != nil {
		return nil
	}
	return describeFieldMap(fm)
}

// nestedTypeName returns the Go type name of a nested struct (or its
// element, if pointer). Empty for anonymous / non-struct types.
func nestedTypeName(t reflect.Type) string {
	if t == nil {
		return ""
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if isJSONRawMessage(t) {
		return ""
	}
	if t.Kind() != reflect.Struct {
		return ""
	}
	return t.Name()
}

// collectNestedTypes walks t recursively and emits a flat
// (typeName → fields) map for every NAMED struct type reachable
// through struct/pointer/slice/array indirection. The root type is
// included if it has a name; anonymous/non-struct types are skipped.
// Visited names short-circuit, so cyclic graphs (User→Org→User) don't
// loop. Maps + json.RawMessage are leaf-typed at the parent and not
// recursed into.
//
// Used by Engine.Describe to populate MethodDescriptor.NestedTypes so
// the IntrospectReport's type catalog covers every type the generated
// client references (the original bug: AuthzCheckParams.Claims *Claims
// surfaced as a typeName but Claims itself was never shipped).
func collectNestedTypes(t reflect.Type, out map[string][]ParamField) {
	if t == nil || out == nil {
		return
	}
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		collectNestedTypes(t.Elem(), out)
		return
	}
	if t.Kind() != reflect.Struct {
		return
	}
	if isJSONRawMessage(t) {
		return
	}
	if isStdlibType(t) {
		// Skip stdlib structs (time.Time, time.Location, etc.) —
		// they marshal to JSON primitives anyway and don't belong
		// in the user-facing type catalog.
		return
	}
	name := t.Name()
	if name == "" {
		return
	}
	if _, seen := out[name]; seen {
		return
	}
	fields := describeStructFields(t)
	out[name] = fields
	// Recurse into each struct field's type.
	for i := 0; i < t.NumField(); i++ {
		collectNestedTypes(t.Field(i).Type, out)
	}
}

// isStdlibType reports whether t lives in a Go stdlib package. Stdlib
// package paths have no "." (no module domain); user-code paths look
// like "github.com/foo/bar". Used by the nested-type walker to skip
// time.Time and other stdlib structs that don't belong in the
// IntrospectReport type catalog.
func isStdlibType(t reflect.Type) bool {
	p := t.PkgPath()
	if p == "" {
		return false
	}
	for i := 0; i < len(p); i++ {
		if p[i] == '.' {
			return false
		}
		if p[i] == '/' {
			break
		}
	}
	return true
}

// elemTypeName returns the named element type of a slice/array, or "".
func elemTypeName(t reflect.Type) string {
	if t == nil {
		return ""
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Slice && t.Kind() != reflect.Array {
		return ""
	}
	return nestedTypeName(t.Elem())
}
