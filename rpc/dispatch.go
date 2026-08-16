package rpc

import (
	"encoding/json"
	"reflect"
)

// Dispatch is the transport-agnostic entry. Adapters parse HTTP
// path/body, build a *Context, then call Dispatch. The return is the
// wire status and the JSON envelope already encoded — adapters set
// Content-Type and write the bytes.
//
// body is the raw request body, typically `{"args":[...]}` or
// `{"args":{...}}`. An empty body is treated as no-args so methods that
// take no params can be invoked with no body.
func (e *Engine) Dispatch(ctx *Context, router, method string, body []byte) (status int, respBody []byte) {
	codec := e.codecForContext(ctx)
	entry, ok := e.lookup(router, method)
	if !ok {
		if !e.HasRouter(router) {
			return encodeErrorWith(codec, NotFound("router %q not found", router))
		}
		// Wire method names are lowerFirst(GoName) (List → list). A caller
		// sending the Go casing gets a bare "not found" that reads like a
		// missing method — hint the correct wire name instead of making
		// them re-derive the casing rule. See HELL-282.
		if suggestion := e.suggestMethod(router, method); suggestion != "" {
			return encodeErrorWith(codec, NotFound("method %q not found on router %q; did you mean %q?", method, router, suggestion))
		}
		return encodeErrorWith(codec, NotFound("method %q not found on router %q", method, router))
	}

	// Typed fast path (rpc.Handle): a closure built at boot calls the
	// handler directly — no reflect.Value.Call, no reflect.New.
	if entry.invoke != nil {
		return entry.invoke(ctx, body)
	}

	args := []reflect.Value{entry.router, reflect.ValueOf(ctx)}
	var paramPtr any
	if entry.hasParams {
		ptr := reflect.New(entry.paramType)
		if derr := codec.DecodeParams(body, ptr.Interface(), entry.fieldMap); derr != nil {
			return encodeErrorWith(codec, asRPCError(derr, BadRequest("%v", derr)))
		}
		// Header-sourced fields bind AFTER the codec decodes the body (they
		// are not body fields). No-op unless the params struct has header=
		// fields.
		if herr := bindHeaderFields(ptr, entry.fieldMap, ctx); herr != nil {
			return encodeErrorWith(codec, herr)
		}
		args = append(args, ptr)
		paramPtr = ptr.Interface()
	}

	// Resource-scoped authz hook (HELL-283): the router may authorize the
	// call in-process now that params are decoded and its store is reachable,
	// before the handler runs. Deny surfaces verbatim; the handler is skipped.
	if ma := e.authorizerFor(router); ma != nil {
		if err := ma.AuthorizeMethod(ctx, method, paramPtr); err != nil {
			return encodeErrorWith(codec, asRPCError(err, Internal("authorization failed")))
		}
	}

	results := entry.method.Func.Call(args)

	errVal := results[len(results)-1]
	if !errVal.IsNil() {
		callErr := errVal.Interface().(error)
		return encodeErrorWith(codec, asRPCError(callErr, &Error{Status: 500, Code: "INTERNAL", Message: "internal server error"}))
	}

	var data any
	if len(results) == 2 {
		data = results[0].Interface()
	}
	body2, mErr := codec.EncodeResult(data)
	if mErr != nil {
		return encodeErrorWith(codec, Internal("encode result: %v", mErr))
	}
	return 200, body2
}

// bindParams decodes raw into the destination struct value, picking the
// positional or named path per the first non-whitespace byte. fm may be
// nil for legacy/no-tag callers — in that case we fall back to plain
// json.Unmarshal so the engine stays usable without struct-tag wiring.
func bindParams(dst reflect.Value, fm *FieldMap, raw json.RawMessage) *Error {
	trimmed := trimSpaceJSON(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	first := trimmed[0]
	switch first {
	case '[':
		return decodeFromArray(dst, fm, trimmed)
	case '{':
		return decodeFromObject(dst, fm, trimmed)
	default:
		return BadRequest("args must be an array or object, got %q", string(trimmed[:1]))
	}
}

// decodeFromArray binds positional args. Each entry at index i maps to
// the field at FieldMap.ByPos[i].
//
// Backward-compat: when the array has exactly one element AND that
// element is itself a JSON object, the entire element is treated as a
// named-object body. This preserves the historical `{"args":[{...}]}`
// shape — every sov caller and bwee-2 caller wraps the params struct
// in a one-element array, and that idiom continues to decode the same
// way.
func decodeFromArray(dst reflect.Value, fm *FieldMap, raw json.RawMessage) *Error {
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return BadRequest("invalid positional args: %v", err)
	}
	if len(entries) == 0 {
		if fm != nil {
			for _, f := range fm.Fields {
				if f.HeaderSource != "" {
					continue // header fields are enforced by bindHeaderFields, not the body
				}
				if f.Required {
					return BadRequest("field %q is required", f.WireName)
				}
			}
		}
		return nil
	}
	if len(entries) == 1 {
		first := trimSpaceJSON(entries[0])
		if len(first) > 0 && first[0] == '{' {
			return decodeFromObject(dst, fm, first)
		}
	}
	if fm == nil {
		// No field map and not the single-object shape — historical
		// dispatch only supported one params struct via args[0], so this
		// branch is unreachable in practice. Decode args[0] as the
		// params struct to keep the no-tag path working.
		if err := json.Unmarshal(entries[0], dst.Addr().Interface()); err != nil {
			return BadRequest("invalid params: %v", err)
		}
		return nil
	}
	if len(entries) > len(fm.ByPos) {
		return BadRequest("too many positional args: got %d, want at most %d", len(entries), len(fm.ByPos))
	}
	for i, raw := range entries {
		idx := fm.ByPos[i]
		if idx < 0 {
			return BadRequest("no field bound to positional slot %d", i)
		}
		f := fm.Fields[idx]
		fv := dst.Field(f.StructIdx)
		if len(raw) == 0 || string(raw) == "null" {
			if f.Required {
				return BadRequest("field %q is required", f.WireName)
			}
			continue
		}
		if err := json.Unmarshal(raw, fv.Addr().Interface()); err != nil {
			return BadRequest("decode positional arg %d (%s): %v", i, f.WireName, err)
		}
	}
	// Enforce required fields that didn't appear in the array.
	for i := len(entries); i < len(fm.ByPos); i++ {
		idx := fm.ByPos[i]
		if idx < 0 {
			continue
		}
		f := fm.Fields[idx]
		if f.Required {
			return BadRequest("field %q is required", f.WireName)
		}
	}
	return nil
}

// decodeFromObject binds named args. Keys map via FieldMap.ByName.
// Unknown keys are silently ignored (forward-compat: clients can send
// fields the server doesn't yet know about). Excluded fields (sov:"-")
// are not in ByName so they cannot be set from the wire — wire forgery
// defense.
func decodeFromObject(dst reflect.Value, fm *FieldMap, raw json.RawMessage) *Error {
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return BadRequest("invalid named args: %v", err)
	}
	if fm == nil {
		if err := json.Unmarshal(raw, dst.Addr().Interface()); err != nil {
			return BadRequest("invalid params: %v", err)
		}
		return nil
	}
	for name, value := range entries {
		idx, ok := fm.ByName[name]
		if !ok {
			continue
		}
		f := fm.Fields[idx]
		fv := dst.Field(f.StructIdx)
		if len(value) == 0 || string(value) == "null" {
			if f.Required {
				return BadRequest("field %q is required", f.WireName)
			}
			continue
		}
		if err := json.Unmarshal(value, fv.Addr().Interface()); err != nil {
			return BadRequest("decode field %q: %v", f.WireName, err)
		}
	}
	for _, f := range fm.Fields {
		if f.HeaderSource != "" || !f.Required {
			continue // header fields are enforced by bindHeaderFields, not the body
		}
		if _, ok := entries[f.WireName]; !ok {
			return BadRequest("field %q is required", f.WireName)
		}
	}
	return nil
}

// trimSpaceJSON returns raw with leading whitespace stripped. Avoids
// allocating; just narrows the slice.
func trimSpaceJSON(raw json.RawMessage) json.RawMessage {
	for i, b := range raw {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		default:
			return raw[i:]
		}
	}
	return nil
}
