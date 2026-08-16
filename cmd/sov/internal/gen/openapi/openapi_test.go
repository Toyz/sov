package openapi

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/rpc"
)

func sampleReport() *gateway.IntrospectReport {
	return &gateway.IntrospectReport{
		Services: map[string][]rpc.RouterDescriptor{
			"Chirp": {{
				Router: "Chirp", Title: "Chirp",
				Methods: []rpc.MethodDescriptor{{
					Method: "post", Title: "Post", PostPath: "/rpc/Chirp/post",
					HasParams: true,
					Params: []rpc.ParamField{
						{JSONName: "body", SchemaType: "string"},
						{JSONName: "tenant", SchemaType: "string", Source: "header", Header: "X-Tenant-Id"},
						{JSONName: "draft", SchemaType: "boolean", Omitempty: true},
					},
					ResponseTypeName:   "Chirp",
					ResponseTypeScript: "Chirp",
				}},
			}},
		},
		Types: map[string]gateway.TypeDescriptor{
			"Chirp": {Name: "Chirp", Fields: []rpc.ParamField{
				{JSONName: "id", SchemaType: "string"},
				{JSONName: "body", SchemaType: "string"},
			}},
		},
	}
}

func emitToMap(t *testing.T) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	Emit(&buf, "Test API", sampleReport())
	var spec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &spec); err != nil {
		t.Fatalf("emitted spec is not valid JSON: %v\n%s", err, buf.String())
	}
	return spec
}

func obj(t *testing.T, v any, path string) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("expected object at %s, got %T", path, v)
	}
	return m
}

func TestEmit_ValidEnvelopeAndPaths(t *testing.T) {
	spec := emitToMap(t)
	if spec["openapi"] != "3.0.3" {
		t.Fatalf("openapi version: %v", spec["openapi"])
	}
	paths := obj(t, spec["paths"], "paths")
	post := obj(t, obj(t, paths["/rpc/Chirp/post"], "path")["post"], "post")
	if post["operationId"] != "Chirp_post" {
		t.Fatalf("operationId: %v", post["operationId"])
	}
}

func TestEmit_HeaderFieldLiftedOutOfBody(t *testing.T) {
	spec := emitToMap(t)
	post := obj(t, obj(t, obj(t, spec["paths"], "paths")["/rpc/Chirp/post"], "path")["post"], "post")

	// The header field must appear as an in:header parameter, not in the body.
	params, ok := post["parameters"].([]any)
	if !ok || len(params) != 1 {
		t.Fatalf("expected 1 header parameter, got %v", post["parameters"])
	}
	hp := obj(t, params[0], "param")
	if hp["in"] != "header" || hp["name"] != "X-Tenant-Id" {
		t.Fatalf("header param: %v", hp)
	}

	body := obj(t, post["requestBody"], "requestBody")
	schema := obj(t, obj(t, obj(t, body["content"], "content")["application/json"], "json")["schema"], "schema")
	args := obj(t, obj(t, schema["properties"], "props")["args"], "args")
	item := obj(t, args["items"], "items")
	props := obj(t, item["properties"], "item.properties")
	if _, ok := props["tenant"]; ok {
		t.Fatal("header field 'tenant' must NOT appear in the request body")
	}
	if _, ok := props["body"]; !ok {
		t.Fatal("body field 'body' should be present")
	}
	if _, ok := props["draft"]; !ok {
		t.Fatal("body field 'draft' should be present")
	}

	// 'body' is required (present always); 'draft' is omitempty → optional.
	req, _ := item["required"].([]any)
	got := map[string]bool{}
	for _, r := range req {
		got[r.(string)] = true
	}
	if !got["body"] {
		t.Fatal("'body' should be required")
	}
	if got["draft"] {
		t.Fatal("'draft' (omitempty) should NOT be required")
	}
}

func TestEmit_ResponseRefAndSchemas(t *testing.T) {
	spec := emitToMap(t)
	post := obj(t, obj(t, obj(t, spec["paths"], "paths")["/rpc/Chirp/post"], "path")["post"], "post")
	resp200 := obj(t, obj(t, post["responses"], "responses")["200"], "200")
	schema := obj(t, obj(t, obj(t, resp200["content"], "content")["application/json"], "json")["schema"], "schema")
	data := obj(t, obj(t, schema["properties"], "props")["data"], "data")
	if data["$ref"] != "#/components/schemas/Chirp" {
		t.Fatalf("response data ref: %v", data["$ref"])
	}
	schemas := obj(t, obj(t, spec["components"], "components")["schemas"], "schemas")
	if _, ok := schemas["Chirp"]; !ok {
		t.Fatal("components.schemas.Chirp missing")
	}
}
