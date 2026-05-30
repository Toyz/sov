package rpc

import (
	"encoding/json"
	"errors"
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
		var p P
		if hasParams && len(body) > 0 {
			if perr := decodeTypedParams(reflect.ValueOf(&p).Elem(), fm, body); perr != nil {
				return writeErr(perr)
			}
		}
		r, err := fn(ctx, &p)
		if err != nil {
			return typedErr(err)
		}
		return 200, MarshalSuccess(r)
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
		var p P
		if hasParams && len(body) > 0 {
			if perr := decodeTypedParams(reflect.ValueOf(&p).Elem(), fm, body); perr != nil {
				return writeErr(perr)
			}
		}
		if err := fn(ctx, &p); err != nil {
			return typedErr(err)
		}
		return 200, MarshalSuccess(nil)
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
	return fm, true
}

// decodeTypedParams unwraps the {"args":...} envelope and binds into dst
// via the boot-built field map (same dual-shape semantics as Register).
func decodeTypedParams(dst reflect.Value, fm *FieldMap, body []byte) *Error {
	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		return BadRequest("invalid request body: %v", err)
	}
	return bindParams(dst, fm, req.Args)
}

func typedErr(err error) (int, []byte) {
	var rpcErr *Error
	if errors.As(err, &rpcErr) {
		return rpcErr.Status, MarshalError(rpcErr)
	}
	return 500, MarshalError(&Error{Status: 500, Code: "INTERNAL", Message: "internal server error"})
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
