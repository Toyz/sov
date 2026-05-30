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
	return &Error{Message: fmt.Sprintf(msg, args...), Code: "RATE_LIMITED", Status: 429}
}
