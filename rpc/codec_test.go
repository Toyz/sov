package rpc

import (
	"strings"
	"testing"
)

// spyCodec wraps the JSON codec and counts calls so tests can prove the
// dispatch seam actually delegates to the installed codec.
type spyCodec struct {
	decodes, encodes, encErrs int
	inner                     Codec
}

func (c *spyCodec) Name() string { return "spy" }
func (c *spyCodec) DecodeParams(body []byte, p any, fm *FieldMap) error {
	c.decodes++
	return c.inner.DecodeParams(body, p, fm)
}
func (c *spyCodec) EncodeResult(d any) ([]byte, error) { c.encodes++; return c.inner.EncodeResult(d) }
func (c *spyCodec) EncodeError(e *Error) ([]byte, error) {
	c.encErrs++
	return c.inner.EncodeError(e)
}

func TestCodec_SeamInvoked_ReflectPath(t *testing.T) {
	e := NewEngine()
	spy := &spyCodec{inner: jsonCodec{}}
	e.SetCodec(spy)
	e.Register(&EchoRouter{Prefix: "pre:"})

	st, body := e.Dispatch(&Context{}, "Echo", "say", []byte(`{"args":{"msg":"hi"}}`))
	if st != 200 {
		t.Fatalf("st=%d body=%s", st, body)
	}
	if spy.decodes == 0 || spy.encodes == 0 {
		t.Fatalf("codec not invoked on reflect path: decodes=%d encodes=%d", spy.decodes, spy.encodes)
	}
	if !strings.Contains(string(body), "pre:hi") {
		t.Fatalf("body=%s", body)
	}
}

func TestCodec_SeamInvoked_TypedPath(t *testing.T) {
	e := NewEngine()
	spy := &spyCodec{inner: jsonCodec{}}
	e.SetCodec(spy)
	Handle(e, "Calc", "double", func(ctx *Context, p *struct {
		N int `json:"n"`
	}) (int, error) {
		return p.N * 2, nil
	})

	st, body := e.Dispatch(&Context{}, "Calc", "double", []byte(`{"args":{"n":21}}`))
	if st != 200 {
		t.Fatalf("st=%d body=%s", st, body)
	}
	if spy.decodes == 0 || spy.encodes == 0 {
		t.Fatalf("codec not invoked on typed path: decodes=%d encodes=%d", spy.decodes, spy.encodes)
	}
	if !strings.Contains(string(body), "42") {
		t.Fatalf("body=%s", body)
	}
}

// rawCodec proves the codec OWNS the wire format: it emits non-JSON bytes,
// and dispatch returns exactly what the codec produced (no forced JSON).
type rawCodec struct{}

func (rawCodec) Name() string { return "raw" }
func (rawCodec) DecodeParams(body []byte, p any, fm *FieldMap) error {
	return jsonCodec{}.DecodeParams(body, p, fm) // still accept JSON args for the test
}
func (rawCodec) EncodeResult(d any) ([]byte, error) { return []byte("RAW-OK"), nil }
func (rawCodec) EncodeError(e *Error) ([]byte, error) {
	return []byte("RAW-ERR:" + e.Message), nil
}

func TestCodec_CustomEncodingOwnsBody(t *testing.T) {
	e := NewEngine()
	e.SetCodec(rawCodec{})
	e.Register(&EchoRouter{Prefix: "p:"})

	st, body := e.Dispatch(&Context{}, "Echo", "ping", nil)
	if st != 200 || string(body) != "RAW-OK" {
		t.Fatalf("success body not codec-owned: st=%d body=%q", st, body)
	}

	// Handler error must also route through the codec's EncodeError.
	st, body = e.Dispatch(&Context{}, "Echo", "refuse", nil)
	if st != 403 || !strings.HasPrefix(string(body), "RAW-ERR:") {
		t.Fatalf("error body not codec-owned: st=%d body=%q", st, body)
	}
}

func TestCodec_ErrorRoutedThroughCodec(t *testing.T) {
	e := NewEngine()
	spy := &spyCodec{inner: jsonCodec{}}
	e.SetCodec(spy)
	e.Register(&EchoRouter{Prefix: "p:"})

	// Unknown method -> NotFound must also go through the codec.
	st, _ := e.Dispatch(&Context{}, "Echo", "nope", nil)
	if st != 404 {
		t.Fatalf("st=%d", st)
	}
	if spy.encErrs == 0 {
		t.Fatalf("not-found error did not route through codec.EncodeError")
	}
}

func TestCodec_DefaultIsJSON(t *testing.T) {
	e := NewEngine()
	if e.activeCodec().Name() != "json" {
		t.Fatalf("default codec = %q, want json", e.activeCodec().Name())
	}
	// SetCodec(nil) falls back to JSON, never a nil codec.
	e.SetCodec(nil)
	if e.activeCodec().Name() != "json" {
		t.Fatalf("SetCodec(nil) did not fall back to json")
	}
}
