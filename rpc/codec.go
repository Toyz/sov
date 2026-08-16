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
// dispatch seam. Framework envelopes (_batch, _introspect, batch/MCP entry
// dispatches, and the authz Check / auth verify sub-dispatches) are pinned to
// JSON: they all carry Content-Type: application/json, which the gateway
// resolves to the registered json codec — never to a SetCodec-swapped default.
// A custom default codec therefore never re-encodes framework envelopes. See
// HELL-286.
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

// jsonName is the registry key and negotiation id of the default codec.
const jsonName = "json"

func (jsonCodec) Name() string { return jsonName }

// maxJSONDepth bounds inbound object/array nesting. Real params nest far
// shallower; the cap rejects a decode-amplification payload (deeply nested
// arrays/objects that slip under MaxBodyBytes but force worst-case decoder
// allocation) before json.Unmarshal runs.
const maxJSONDepth = 64

func (jsonCodec) DecodeParams(body []byte, params any, fm *FieldMap) error {
	if len(body) == 0 {
		return nil
	}
	if !jsonDepthOK(body, maxJSONDepth) {
		return BadRequest("request body nests too deeply (max %d)", maxJSONDepth)
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

// jsonDepthOK reports whether the JSON in b nests no deeper than max object/array
// levels. String-aware: brackets inside string literals don't count, and escaped
// quotes are handled. A cheap O(n) scan with no allocation.
func jsonDepthOK(b []byte, max int) bool {
	depth := 0
	inStr := false
	esc := false
	for _, c := range b {
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{', '[':
			depth++
			if depth > max {
				return false
			}
		case '}', ']':
			depth--
		}
	}
	return true
}

func (jsonCodec) EncodeError(e *Error) ([]byte, error) { return MarshalError(e), nil }

// DefaultCodec returns the codec an Engine uses when none is installed.
func DefaultCodec() Codec { return jsonCodec{} }
