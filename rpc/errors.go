package rpc

import "fmt"

// Error is the canonical error type returned by router methods.
//
// Status maps to the HTTP status code the transport adapter sets.
// Code is the UPPERCASE_SNAKE category (BAD_REQUEST, NOT_FOUND, ...);
// it surfaces as JSON `"code"`. ErrorCode is an optional stable
// application-level reason ("WORKSPACE_SLUG_IN_USE") for client branching;
// it surfaces as JSON `"error_code"`.
type Error struct {
	Message   string `json:"message"`
	Code      string `json:"code,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
	Status    int    `json:"-"`
	// Retryable signals the caller MAY safely retry — a transient failure
	// (rate limit, upstream unavailable, timeout). false means permanent; a
	// client should not retry. Generated clients key their retry logic on this.
	Retryable bool `json:"retryable,omitempty"`
	// RetryAfter, when > 0, is the seconds a client should wait before retrying.
	RetryAfter int `json:"retry_after,omitempty"`
	// Details carries per-field failures so a client can map a BAD_REQUEST to
	// the exact form field that failed instead of a single opaque message.
	Details []FieldError `json:"details,omitempty"`
}

// FieldError is one field-level failure, carried in Error.Details.
type FieldError struct {
	Field   string `json:"field"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}

// Retry marks the error retryable, optionally with a retry-after (seconds), and
// returns it for chaining: return rpc.Internal("upstream blip").Retry(1).
func (e *Error) Retry(afterSeconds ...int) *Error {
	e.Retryable = true
	if len(afterSeconds) == 1 {
		e.RetryAfter = afterSeconds[0]
	}
	return e
}

// WithDetails attaches field-level failures and returns the error for chaining.
func (e *Error) WithDetails(d ...FieldError) *Error {
	e.Details = append(e.Details, d...)
	return e
}

func (e *Error) Error() string { return e.Message }

// NotFound returns 404 NOT_FOUND.
func NotFound(msg string, args ...any) *Error {
	return &Error{Message: fmt.Sprintf(msg, args...), Code: "NOT_FOUND", Status: 404}
}

// Forbidden returns 403 FORBIDDEN.
func Forbidden(msg string, args ...any) *Error {
	return &Error{Message: fmt.Sprintf(msg, args...), Code: "FORBIDDEN", Status: 403}
}

// ForbiddenCode returns 403 FORBIDDEN with a stable application error_code.
func ForbiddenCode(errorCode, msg string, args ...any) *Error {
	return &Error{Message: fmt.Sprintf(msg, args...), Code: "FORBIDDEN", ErrorCode: errorCode, Status: 403}
}

// Unauthorized returns 401 UNAUTHORIZED.
func Unauthorized(msg string, args ...any) *Error {
	return &Error{Message: fmt.Sprintf(msg, args...), Code: "UNAUTHORIZED", Status: 401}
}

// BadRequest returns 400 BAD_REQUEST.
func BadRequest(msg string, args ...any) *Error {
	return &Error{Message: fmt.Sprintf(msg, args...), Code: "BAD_REQUEST", Status: 400}
}

// BadRequestCode returns 400 BAD_REQUEST with a stable application error_code.
func BadRequestCode(errorCode, msg string, args ...any) *Error {
	return &Error{Message: fmt.Sprintf(msg, args...), Code: "BAD_REQUEST", ErrorCode: errorCode, Status: 400}
}

// Conflict returns 409 CONFLICT.
func Conflict(msg string, args ...any) *Error {
	return &Error{Message: fmt.Sprintf(msg, args...), Code: "CONFLICT", Status: 409}
}

// Internal returns 500 INTERNAL. The Message is logged server-side; the
// transport adapter substitutes a generic message on the wire so internal
// detail does not leak.
func Internal(msg string, args ...any) *Error {
	return &Error{Message: fmt.Sprintf(msg, args...), Code: "INTERNAL", Status: 500}
}

// NotImplemented returns 501 NOT_IMPLEMENTED. Use for RPC stubs.
func NotImplemented(msg string, args ...any) *Error {
	return &Error{Message: fmt.Sprintf(msg, args...), Code: "NOT_IMPLEMENTED", Status: 501}
}

// TooManyRequests returns 429 RATE_LIMITED.
func TooManyRequests(msg string, args ...any) *Error {
	return &Error{Message: fmt.Sprintf(msg, args...), Code: "RATE_LIMITED", Status: 429, Retryable: true}
}
