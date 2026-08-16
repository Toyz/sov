package rpc

import (
	"reflect"
)

// Handle registers a typed RPC method with NO reflection in the dispatch
// hot path. The handler is called directly through a closure built once at
// boot — no reflect.Value.Call, no reflect.New per request. Reflection is
// used only here, at registration, to build the introspect descriptor and
// the param field map.
//
//	rpc.Handle(eng, "Chirp", "post",
//	    func(ctx *rpc.Context, p *chirps.PostParams) (*chirps.Chirp, error) { ... })
//
// Two wins over the reflective Register path:
//   - the handler signature is checked at COMPILE time (a wrong shape is a
//     build error, not a boot panic); and
//   - dispatch skips method-invoke reflection (the part that hurt).
//
// Field decoding still uses the boot-built FieldMap, so both wire arg
// shapes (positional + named) and `sov` tags work identically to Register.
// Handle and Register coexist on the same Engine; use Handle for hot
// methods you want type-checked and reflection-free to call.
//
// For methods that return only an error, use HandleErr. No-arg methods are
// cheap to dispatch reflectively — keep them on Register, or pass a
// zero-field params struct.
func Handle[P any, R any](e *Engine, router, method string, fn func(ctx *Context, p *P) (R, error)) {
	pt := reflect.TypeFor[P]()
	fm, hasParams := typedParamMap(pt, router, method)
	entry := &methodEntry{
		goName:     upperFirst(method),
		wireName:   method,
		hasParams:  hasParams,
		resultType: reflect.TypeFor[R](),
	}
	if hasParams {
		entry.paramType = pt
		entry.fieldMap = fm
	}
	entry.invoke = func(ctx *Context, body []byte) (int, []byte) {
		codec := e.codecForContext(ctx)
		var p P
		if hasParams {
			if derr := codec.DecodeParams(body, &p, fm); derr != nil {
				return encodeErrorWith(codec, asRPCError(derr, BadRequest("%v", derr)))
			}
			// Header-sourced fields bind after the body decode. Gate on the
			// precomputed slice so the reflection-free fast path stays
			// alloc-free when the params struct has no header= fields.
			if len(fm.HeaderFields) > 0 {
				if herr := bindHeaderFields(reflect.ValueOf(&p), fm, ctx); herr != nil {
					return encodeErrorWith(codec, herr)
				}
			}
		}
		r, err := fn(ctx, &p)
		if err != nil {
			return encodeErrorWith(codec, asRPCError(err, &Error{Status: 500, Code: "INTERNAL", Message: "internal server error"}))
		}
		out, mErr := codec.EncodeResult(r)
		if mErr != nil {
			return encodeErrorWith(codec, Internal("encode result: %v", mErr))
		}
		return 200, out
	}
	e.registerTyped(router, method, entry)
}

// HandleErr registers a typed method that returns only an error (no result
// body). Same boot-time, reflection-free dispatch as Handle.
func HandleErr[P any](e *Engine, router, method string, fn func(ctx *Context, p *P) error) {
	pt := reflect.TypeFor[P]()
	fm, hasParams := typedParamMap(pt, router, method)
	entry := &methodEntry{
		goName:    upperFirst(method),
		wireName:  method,
		hasParams: hasParams,
	}
	if hasParams {
		entry.paramType = pt
		entry.fieldMap = fm
	}
	entry.invoke = func(ctx *Context, body []byte) (int, []byte) {
		codec := e.codecForContext(ctx)
		var p P
		if hasParams {
			if derr := codec.DecodeParams(body, &p, fm); derr != nil {
				return encodeErrorWith(codec, asRPCError(derr, BadRequest("%v", derr)))
			}
			// Header-sourced fields bind after the body decode. Gate on the
			// precomputed slice so the reflection-free fast path stays
			// alloc-free when the params struct has no header= fields.
			if len(fm.HeaderFields) > 0 {
				if herr := bindHeaderFields(reflect.ValueOf(&p), fm, ctx); herr != nil {
					return encodeErrorWith(codec, herr)
				}
			}
		}
		if err := fn(ctx, &p); err != nil {
			return encodeErrorWith(codec, asRPCError(err, &Error{Status: 500, Code: "INTERNAL", Message: "internal server error"}))
		}
		out, mErr := codec.EncodeResult(nil)
		if mErr != nil {
			return encodeErrorWith(codec, Internal("encode result: %v", mErr))
		}
		return 200, out
	}
	e.registerTyped(router, method, entry)
}

// typedParamMap builds the field map for P. hasParams is false when P has
// no fields (e.g. struct{}), matching the reflective no-params behavior.
func typedParamMap(pt reflect.Type, router, method string) (*FieldMap, bool) {
	if pt.Kind() != reflect.Struct || pt.NumField() == 0 {
		return nil, false
	}
	fm, err := BuildFieldMap(pt)
	if err != nil {
		panic("rpc.Handle: " + router + "." + method + " params " + pt.String() + ": " + err.Error())
	}
	if err := RejectNestedHeaders(pt); err != nil {
		panic("rpc.Handle: " + router + "." + method + " params " + pt.String() + ": " + err.Error())
	}
	return fm, true
}

// registerTyped installs a typed entry under router/method, creating the
// router bucket on first use. Panics on duplicate method (boot-time, like
// Register).
func (e *Engine) registerTyped(router, method string, entry *methodEntry) {
	e.mu.Lock()
	defer e.mu.Unlock()
	methods, ok := e.routers[router]
	if !ok {
		methods = map[string]*methodEntry{}
		e.routers[router] = methods
		e.routerOrder = append(e.routerOrder, router)
	}
	if _, dup := methods[method]; dup {
		panic("rpc.Handle: " + router + "." + method + " already registered")
	}
	if entry.fieldMap != nil && len(entry.fieldMap.HeaderFields) > 0 {
		e.needsHeaderGetter.Store(true)
	}
	methods[method] = entry
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if r[0] >= 'a' && r[0] <= 'z' {
		r[0] -= 'a' - 'A'
	}
	return string(r)
}
