package rpc

import "encoding/json"

// Request is the canonical wire body. The `args` field accepts either
// of two shapes; clients pick per request:
//
//   - Positional:  {"args":[v0, v1, v2]}      — bound by sov tag position
//     (or source order when no
//     sov tag is present)
//   - Named:       {"args":{"name":v, ...}}   — bound by sov tag name
//     (or json tag, or
//     snake_case(GoFieldName))
//
// Both decode into the same Go params struct; dispatch picks the path
// by inspecting the first non-whitespace byte (`[` vs `{`).
type Request struct {
	Args json.RawMessage `json:"args"`
}

// SuccessResponse is the canonical success envelope.
type SuccessResponse struct {
	Data any `json:"data"`
}

// ErrorResponse is the canonical failure envelope.
type ErrorResponse struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Message   string `json:"message"`
	Code      string `json:"code,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
}

// MarshalError builds the JSON body for an Error. Transport adapters use
// this when writing the response.
func MarshalError(e *Error) []byte {
	body, _ := json.Marshal(ErrorResponse{Error: errorBody{
		Message:   e.Message,
		Code:      e.Code,
		ErrorCode: e.ErrorCode,
	}})
	return body
}

// MarshalSuccess builds the JSON body for a successful result.
func MarshalSuccess(data any) []byte {
	body, _ := json.Marshal(SuccessResponse{Data: data})
	return body
}

// DecodeErrorBody parses an `{"error":{message,code,error_code}}` envelope
// into an *Error stamped with status. ok is false when body is not a valid
// JSON error envelope — the caller then supplies its own fallback. Shared
// by the client, the auth verifier, and rpctest so the error-envelope
// shape lives in one place.
func DecodeErrorBody(body []byte, status int) (e *Error, ok bool) {
	var env ErrorResponse
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, false
	}
	return &Error{
		Status:    status,
		Code:      env.Error.Code,
		ErrorCode: env.Error.ErrorCode,
		Message:   env.Error.Message,
	}, true
}

// DecodeEnvelope is the canonical client-side response decoder. For
// status >= 400 it returns the decoded *Error (falling back to an INTERNAL
// error carrying the raw body when the envelope won't parse). For success
// it unmarshals the `data` field into out (a nil out, empty data, or null
// data is a no-op success).
func DecodeEnvelope(body []byte, status int, out any) error {
	if status >= 400 {
		if e, ok := DecodeErrorBody(body, status); ok {
			return e
		}
		return &Error{Status: status, Code: "INTERNAL", Message: string(body)}
	}
	if out == nil {
		return nil
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return Internal("decode envelope: %v", err)
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return nil
	}
	return json.Unmarshal(env.Data, out)
}
