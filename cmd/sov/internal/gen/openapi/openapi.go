// Package openapi emits an OpenAPI 3.0 document from a sov gateway's
// introspect catalog, so consumers who want a standard spec (for Swagger
// UI, Postman, a third-party generator) get one from the same catalog that
// drives the TS/Go/Swift/Kotlin/Python clients.
//
// Mapping: each service method becomes a POST operation at its /rpc path.
// The sov wire envelope is preserved — the request body is
// {"args": [<params object>]} — with header-bound params (sov:"header=")
// lifted out as `in: header` parameters, never body fields, matching the
// runtime contract. Named request/response structs become
// components/schemas entries referenced by $ref. ParamField.SchemaType is
// already OpenAPI-shaped (string|number|boolean|array|object), so the type
// mapping is direct.
package openapi

import (
	"encoding/json"
	"io"
	"sort"
	"strings"

	"github.com/Toyz/sov/cmd/sov/internal/catalog"
	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/rpc"
)

// Run executes the openapi subcommand. Thin wrapper over the shared
// catalog.RunEmitter scaffolding; only Emit is openapi-specific.
func Run(argv []string, stdout, stderr io.Writer) int {
	return catalog.RunEmitter(catalog.EmitterSpec{
		Name:           "sovgen openapi",
		DefaultOut:     "./openapi.json",
		OutHelp:        "output .json file; pass \"-\" for stdout",
		DefaultPackage: "Sov API",
		PackageHelp:    "API title (info.title in the spec)",
		Emit:           Emit,
	}, argv, stdout, stderr)
}

// Emit writes the OpenAPI 3.0 document for r to w. title names the API
// (info.title); empty defaults to "Sov API". The signature matches the
// other gen emitters so `sov gen all` can drive it uniformly.
func Emit(w io.Writer, title string, r *gateway.IntrospectReport) {
	if title == "" {
		title = "Sov API"
	}
	spec := map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title":       title,
			"version":     "1.0.0",
			"description": "Generated from the sov gateway introspect catalog. Requests use the sov envelope: POST the method path with body {\"args\":[<params>]}.",
		},
		"paths":      buildPaths(r),
		"components": map[string]any{"schemas": buildSchemas(r)},
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	_ = enc.Encode(spec)
}

func ref(name string) string { return "#/components/schemas/" + name }

func buildPaths(r *gateway.IntrospectReport) map[string]any {
	paths := map[string]any{}
	for _, routers := range r.Services {
		for _, rt := range routers {
			for _, m := range rt.Methods {
				paths[m.PostPath] = map[string]any{"post": buildOperation(rt, m)}
			}
		}
	}
	return paths
}

func buildOperation(rt rpc.RouterDescriptor, m rpc.MethodDescriptor) map[string]any {
	op := map[string]any{
		"operationId": rt.Router + "_" + m.Method,
		"tags":        []string{rt.Router},
		"responses": map[string]any{
			"200":     successResponse(m),
			"default": errorResponse(),
		},
	}
	if m.Title != "" {
		op["summary"] = m.Title
	}
	if m.Deprecated {
		op["deprecated"] = true
		if m.DeprecatedReason != "" {
			op["description"] = "Deprecated: " + m.DeprecatedReason
		}
	}

	var headerParams []any
	var bodyFields []rpc.ParamField
	for _, p := range m.Params {
		if p.Source == "header" {
			headerParams = append(headerParams, headerParam(p))
		} else {
			bodyFields = append(bodyFields, p)
		}
	}
	if len(headerParams) > 0 {
		op["parameters"] = headerParams
	}
	if m.HasParams && len(bodyFields) > 0 {
		op["requestBody"] = map[string]any{
			"required": true,
			"content": map[string]any{
				"application/json": map[string]any{
					"schema": map[string]any{
						"type":     "object",
						"required": []string{"args"},
						"properties": map[string]any{
							// The sov envelope: args is a positional array whose
							// first element is this method's params object.
							"args": map[string]any{
								"type":  "array",
								"items": objectSchema(bodyFields),
							},
						},
					},
				},
			},
		}
	}
	return op
}

func headerParam(p rpc.ParamField) map[string]any {
	t := p.SchemaType
	if t == "" || t == "object" || t == "array" {
		t = "string" // header values are strings on the wire
	}
	param := map[string]any{
		"name":     p.Header,
		"in":       "header",
		"required": !p.Omitempty && !p.Nullable,
		"schema":   map[string]any{"type": t},
	}
	if p.Desc != "" {
		param["description"] = p.Desc
	}
	return param
}

func successResponse(m rpc.MethodDescriptor) map[string]any {
	var data map[string]any
	switch {
	case m.ResponseTypeName != "" && strings.HasSuffix(m.ResponseTypeScript, "[]"):
		data = map[string]any{"type": "array", "items": map[string]any{"$ref": ref(m.ResponseTypeName)}}
	case m.ResponseTypeName != "":
		data = map[string]any{"$ref": ref(m.ResponseTypeName)}
	default:
		data = map[string]any{} // primitive/map/unknown result
	}
	return map[string]any{
		"description": "success",
		"content": map[string]any{
			"application/json": map[string]any{
				"schema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"data": data},
				},
			},
		},
	}
}

func errorResponse() map[string]any {
	return map[string]any{
		"description": "error — the sov envelope carries a non-null error object",
		"content": map[string]any{
			"application/json": map[string]any{
				"schema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"error": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"code":    map[string]any{"type": "string"},
								"message": map[string]any{"type": "string"},
							},
						},
					},
				},
			},
		},
	}
}

func buildSchemas(r *gateway.IntrospectReport) map[string]any {
	schemas := map[string]any{}
	for name, td := range r.Types {
		schemas[name] = objectSchema(td.Fields)
	}
	return schemas
}

// objectSchema builds an OpenAPI object schema from a field list. Header-
// bound fields are skipped — they are not part of the JSON body. A field is
// required iff it is always present on the wire (not omitempty, not
// nullable); ParamField.Required is validation intent, not presence, so it
// is deliberately not used here.
func objectSchema(fields []rpc.ParamField) map[string]any {
	props := map[string]any{}
	var required []string
	for _, f := range fields {
		if f.Source == "header" {
			continue
		}
		props[f.JSONName] = fieldSchema(f)
		if !f.Omitempty && !f.Nullable {
			required = append(required, f.JSONName)
		}
	}
	obj := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		sort.Strings(required)
		obj["required"] = required
	}
	return obj
}

func fieldSchema(f rpc.ParamField) map[string]any {
	switch f.SchemaType {
	case "object":
		if f.TypeName != "" {
			// A $ref takes no sibling keys in OpenAPI 3.0, so metadata is
			// dropped for referenced objects.
			return map[string]any{"$ref": ref(f.TypeName)}
		}
		return withMeta(map[string]any{"type": "object"}, f)
	case "array":
		return withMeta(map[string]any{"type": "array", "items": elemSchema(f)}, f)
	case "string", "number", "boolean":
		return withMeta(map[string]any{"type": f.SchemaType}, f)
	default:
		return withMeta(map[string]any{}, f) // unknown → any
	}
}

func elemSchema(f rpc.ParamField) map[string]any {
	switch {
	case f.ElemType == "object" && f.TypeName != "":
		return map[string]any{"$ref": ref(f.TypeName)}
	case f.ElemType != "" && f.ElemType != "object":
		return map[string]any{"type": f.ElemType}
	default:
		return map[string]any{} // unknown element type
	}
}

func withMeta(s map[string]any, f rpc.ParamField) map[string]any {
	if f.Desc != "" {
		s["description"] = f.Desc
	} else if f.Title != "" {
		s["description"] = f.Title
	}
	if f.Nullable {
		s["nullable"] = true
	}
	if f.Deprecated {
		s["deprecated"] = true
	}
	if f.Example != "" {
		s["example"] = f.Example
	}
	return s
}
