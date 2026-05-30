package gateway

import (
	"fmt"
)

// HookFailure carries everything a RecoveryHandler needs to react to
// a hook failure — panic recovered or error returned. The reaction is
// derived from the returned error: HaltErr → halt boot, RespondErr →
// override the response, anything else → log + continue. Panics are
// treated as continue by default; the RecoveryHandler can re-classify.
type HookFailure struct {
	HookName   string
	PluginName string
	// Err is the returned error OR a wrapped panic. Use HaltErr /
	// RespondErr sentinel checks (errors.As) to read the plugin's
	// intent.
	Err error
	// Panic is the recovered value when this failure came from a
	// panic; nil when the hook returned an error normally.
	Panic any
	// Stack is the goroutine stack trace at the panic site, only
	// populated when Panic != nil.
	Stack []byte
}

// haltError signals the gateway should refuse startup / abort.
// Returned by boot-time hooks via HaltErr.
type haltError struct{ err error }

func (h *haltError) Error() string { return h.err.Error() }
func (h *haltError) Unwrap() error { return h.err }

// HaltErr wraps err so the gateway refuses startup. Boot-time hooks
// (BootValidator, ConfigApplier, LifecycleHook.OnStart) recognize
// this sentinel and bubble it up from ListenAndServe.
//
//	if !cfg.Valid() {
//	    return gateway.HaltErr(fmt.Errorf("config invalid: %s", reason))
//	}
func HaltErr(err error) error {
	if err == nil {
		return nil
	}
	return &haltError{err: err}
}

// IsHalt reports whether err carries a HaltErr sentinel.
func IsHalt(err error) bool {
	_, ok := err.(*haltError)
	if ok {
		return true
	}
	type unwrapper interface{ Unwrap() error }
	for cur := err; cur != nil; {
		if _, ok := cur.(*haltError); ok {
			return true
		}
		u, ok := cur.(unwrapper)
		if !ok {
			return false
		}
		cur = u.Unwrap()
	}
	return false
}

// respondError signals the gateway should return a specific Response
// to the caller. Use RespondErr to wrap.
type respondError struct {
	resp *Response
	err  error
}

func (r *respondError) Error() string       { return r.err.Error() }
func (r *respondError) Unwrap() error       { return r.err }
func (r *respondError) Response() *Response { return r.resp }

// RespondErr wraps err with a *Response the gateway returns to the
// caller instead of a default 500 envelope. Request-path hooks
// (HeaderParser, Middlewarer, RouteHandler, ResponseInterceptor)
// recognize this and short-circuit.
//
//	if origin == "" {
//	    return gateway.RespondErr(&gateway.Response{Status: 400, Body: ...},
//	                              fmt.Errorf("missing Origin header"))
//	}
func RespondErr(resp *Response, err error) error {
	if err == nil {
		err = fmt.Errorf("respond: %d", resp.Status)
	}
	return &respondError{resp: resp, err: err}
}

// ResponseFrom returns the embedded *Response when err was wrapped
// with RespondErr, or nil otherwise.
func ResponseFrom(err error) *Response {
	for cur := err; cur != nil; {
		if re, ok := cur.(*respondError); ok {
			return re.resp
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := cur.(unwrapper)
		if !ok {
			return nil
		}
		cur = u.Unwrap()
	}
	return nil
}
