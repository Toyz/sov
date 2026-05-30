package registertoken

import (
	"testing"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/registertoken/proto"
)

func req(path, token string) *gateway.Request {
	h := gateway.Header{}
	if token != "" {
		h.Set(proto.RegisterTokenHeader, token)
	}
	return &gateway.Request{Path: path, Header: h}
}

func TestRegisterToken_Gate(t *testing.T) {
	p := New(Config{Token: []byte("join-secret")})

	if err := p.ParseHeaders(req("/rpc/_register", "join-secret")); err != nil {
		t.Errorf("correct token should pass, got %v", err)
	}
	if err := p.ParseHeaders(req("/rpc/_register", "wrong")); err == nil {
		t.Error("wrong token must be rejected")
	}
	if err := p.ParseHeaders(req("/rpc/_register", "")); err == nil {
		t.Error("missing token must be rejected")
	}
	// Non-register paths are never gated.
	if err := p.ParseHeaders(req("/rpc/Chirp/post", "")); err != nil {
		t.Errorf("non-register path must pass, got %v", err)
	}
}

func TestRegisterToken_EmptyConfigIsOpen(t *testing.T) {
	p := New(Config{}) // no token → gate disabled
	if err := p.ParseHeaders(req("/rpc/_register", "")); err != nil {
		t.Errorf("empty config must leave register open, got %v", err)
	}
}

func TestVerify_ConstantTimeEdges(t *testing.T) {
	if proto.Verify(nil, []byte("x")) || proto.Verify([]byte("x"), nil) {
		t.Error("empty want or presented must be false")
	}
	if proto.Verify([]byte("abc"), []byte("abc")) != true {
		t.Error("matching tokens must verify")
	}
	if proto.Verify([]byte("abc"), []byte("abd")) {
		t.Error("differing tokens must not verify")
	}
}
