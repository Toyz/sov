package rpc

import (
	"fmt"
	"reflect"
	"strconv"
)

// CtxHeaderGetter is the Context.State key under which the transport adapter
// stashes a HeaderGetter for the current request. The gateway sets it (from the
// inbound request headers); the engine reads it to bind sov:"header=NAME" param
// fields. A package-level key so the gateway sets what the engine reads without
// a shared constants module. See docs/HEADER_PARAMS.md.
const CtxHeaderGetter = "sov.header.getter"

// HeaderGetter returns the value of a request header by name, or "" if absent.
// The transport adapter (gateway) provides one per request; the engine calls it
// to bind fields tagged sov:"header=NAME". Lookup should be case-insensitive.
type HeaderGetter func(name string) string

// headerGetterFrom returns the HeaderGetter stashed on ctx, or nil.
func headerGetterFrom(ctx *Context) HeaderGetter {
	if ctx == nil {
		return nil
	}
	if g, ok := ctx.Get(CtxHeaderGetter).(HeaderGetter); ok {
		return g
	}
	return nil
}

// bindHeaderFields binds every header= field in fm onto the params struct dst
// (a pointer Value) from the request's header getter. Body decoding has already
// run; this is the separate, codec-independent pass for header-sourced fields.
// A required header that is absent/empty is a BadRequest; an optional one is
// left at its zero value. No-op (and no alloc beyond the length check) when the
// params struct has no header fields.
func bindHeaderFields(dst reflect.Value, fm *FieldMap, ctx *Context) *Error {
	if fm == nil || len(fm.HeaderFields) == 0 {
		return nil
	}
	getter := headerGetterFrom(ctx)
	st := dst
	for st.Kind() == reflect.Ptr {
		st = st.Elem()
	}
	for _, idx := range fm.HeaderFields {
		f := fm.Fields[idx]
		var raw string
		if getter != nil {
			raw = getter(f.HeaderSource)
		}
		if raw == "" {
			if f.Required {
				return BadRequest("missing required header %q", f.HeaderSource)
			}
			continue // optional + absent → leave the zero value
		}
		if err := setScalarFromString(st.Field(f.StructIdx), raw); err != nil {
			return BadRequest("header %q: %v", f.HeaderSource, err)
		}
	}
	return nil
}

// setScalarFromString sets a scalar reflect field from a header string. A
// header is a single string, so only scalar kinds (and a pointer to one) are
// supported; a struct/slice/map tagged header= errors here rather than silently
// dropping the value.
func setScalarFromString(fv reflect.Value, s string) error {
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(s)
	case reflect.Bool:
		b, err := strconv.ParseBool(s)
		if err != nil {
			return fmt.Errorf("invalid bool %q", s)
		}
		fv.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid integer %q", s)
		}
		if fv.OverflowInt(n) {
			return fmt.Errorf("integer %q overflows %s", s, fv.Type())
		}
		fv.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid unsigned integer %q", s)
		}
		if fv.OverflowUint(n) {
			return fmt.Errorf("integer %q overflows %s", s, fv.Type())
		}
		fv.SetUint(n)
	case reflect.Float32, reflect.Float64:
		fl, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return fmt.Errorf("invalid number %q", s)
		}
		fv.SetFloat(fl)
	case reflect.Ptr:
		if fv.IsNil() {
			fv.Set(reflect.New(fv.Type().Elem()))
		}
		return setScalarFromString(fv.Elem(), s)
	default:
		return fmt.Errorf("unsupported header field type %s", fv.Type())
	}
	return nil
}
