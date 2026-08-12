package rpc

import (
	"encoding/json"
	"reflect"
)

// Codec encodes and decodes the BODY of an RPC call — the request params
// coming in and the result/error going out. JSON is the default and the
// cross-language PEMM wire (loom-rpc TS <-> sov Go); a consumer may install
// a different codec (e.g. a binary format) for a HOMOGENEOUS deployment
// where both ends agree on it. sov ships ONLY the stdlib-JSON codec — bring
// your own for anything else, so the framework keeps zero external deps.
//
// SCOPE: the codec governs BUSINESS method params/results at the engine
// dispatch seam. Framework envelopes (_batch, _introspect, and the authz
// Check / auth verify sub-dispatches) are JSON by construction and are not
// re-encoded through a custom codec. See HELL-286.
type Codec interface {
	// Name is the codec's wire identity (e.g. "json"), used for
	// Content-Type negotiation.
	Name() string

	// DecodeParams binds a request body into params — a pointer to the
	// method's params struct. fm is the boot-built field layout for that
	// struct (the JSON dual-shape positional/named binding); a codec that
	// frames its own way may ignore it. An EMPTY body means "no args" and
	// MUST be a no-op (leave params zero-valued, return nil).
	DecodeParams(body []byte, params any, fm *FieldMap) error

	// EncodeResult builds the success body for a returned value.
	EncodeResult(data any) ([]byte, error)

	// EncodeError builds the failure body for an *Error.
	EncodeError(e *Error) ([]byte, error)
}

// jsonCodec is the default Codec: the canonical sov JSON wire. It preserves
// the exact behavior dispatch had before the codec seam existed — the
// {"args":...} envelope, dual-shape (positional + named) FieldMap binding,
// and the {"data":...} / {"error":...} envelopes.
type jsonCodec struct{}

func (jsonCodec) Name() string { return "json" }

func (jsonCodec) DecodeParams(body []byte, params any, fm *FieldMap) error {
	if len(body) == 0 {
		return nil
	}
	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		return BadRequest("invalid request body: %v", err)
	}
	// bindParams returns a typed *Error; guard the nil case so we never
	// hand back a non-nil error interface wrapping a nil *Error.
	if perr := bindParams(reflect.ValueOf(params).Elem(), fm, req.Args); perr != nil {
		return perr
	}
	return nil
}

func (jsonCodec) EncodeResult(data any) ([]byte, error) { return MarshalSuccess(data), nil }

func (jsonCodec) EncodeError(e *Error) ([]byte, error) { return MarshalError(e), nil }

// DefaultCodec returns the codec an Engine uses when none is installed.
func DefaultCodec() Codec { return jsonCodec{} }
