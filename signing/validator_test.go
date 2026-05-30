package signing

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"testing"
	"time"
)

type hdr map[string]string

func (h hdr) Get(k string) string { return h[k] }

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	return pub, priv
}

func headersFor(t *testing.T, sid string, priv ed25519.PrivateKey, router, method string, body []byte, ts int64) hdr {
	t.Helper()
	sig := ed25519.Sign(priv, CanonicalMessage(router, method, body, ts))
	return hdr{
		"X-Session": sid,
		"X-Ts":      strconv.FormatInt(ts, 10),
		"X-Sig":     hex.EncodeToString(sig),
	}
}

func TestValidator_HappyPath(t *testing.T) {
	pub, priv := newKey(t)
	store := NewMemoryStore()
	store.Put("sid-1", pub)

	now := time.Unix(1_000_000, 0)
	v := New(Options{Store: store, Now: func() time.Time { return now }})

	body := []byte(`{"args":[{"x":1}]}`)
	h := headersFor(t, "sid-1", priv, "Echo", "say", body, now.Unix())

	sid, err := v.Validate(context.Background(), h, "Echo", "say", body)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if sid != "sid-1" {
		t.Fatalf("sid = %q", sid)
	}
}

func TestValidator_MissingHeaders(t *testing.T) {
	v := New(Options{Store: NewMemoryStore()})
	_, err := v.Validate(context.Background(), hdr{}, "S", "m", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	fail, ok := err.(*Failure)
	if !ok || fail.Reason != ReasonMissingHeaders {
		t.Fatalf("err = %#v", err)
	}
}

func TestValidator_ExpiredTimestamp(t *testing.T) {
	pub, priv := newKey(t)
	store := NewMemoryStore()
	store.Put("sid", pub)

	now := time.Unix(1_000_000, 0)
	v := New(Options{Store: store, Now: func() time.Time { return now }, ReplayWindow: 5 * time.Second})

	body := []byte("{}")
	h := headersFor(t, "sid", priv, "S", "m", body, now.Add(-60*time.Second).Unix())
	_, err := v.Validate(context.Background(), h, "S", "m", body)
	fail, ok := err.(*Failure)
	if !ok || fail.Reason != ReasonExpired {
		t.Fatalf("err = %#v", err)
	}
}

func TestValidator_BadSig(t *testing.T) {
	pub, _ := newKey(t)
	_, priv := newKey(t) // wrong key

	store := NewMemoryStore()
	store.Put("sid", pub)
	now := time.Unix(1_000_000, 0)
	v := New(Options{Store: store, Now: func() time.Time { return now }})

	body := []byte("{}")
	h := headersFor(t, "sid", priv, "S", "m", body, now.Unix())
	_, err := v.Validate(context.Background(), h, "S", "m", body)
	fail, ok := err.(*Failure)
	if !ok || fail.Reason != ReasonInvalidSig {
		t.Fatalf("err = %#v", err)
	}
}

func TestValidator_UnknownSession(t *testing.T) {
	_, priv := newKey(t)
	v := New(Options{Store: NewMemoryStore(), Now: func() time.Time { return time.Unix(1_000_000, 0) }})
	body := []byte("{}")
	h := headersFor(t, "missing", priv, "S", "m", body, 1_000_000)
	_, err := v.Validate(context.Background(), h, "S", "m", body)
	fail, ok := err.(*Failure)
	if !ok || fail.Reason != ReasonSessionMissing {
		t.Fatalf("err = %#v", err)
	}
}

func TestValidator_TamperedBody(t *testing.T) {
	pub, priv := newKey(t)
	store := NewMemoryStore()
	store.Put("sid", pub)
	now := time.Unix(1_000_000, 0)
	v := New(Options{Store: store, Now: func() time.Time { return now }})

	body := []byte(`{"x":1}`)
	h := headersFor(t, "sid", priv, "S", "m", body, now.Unix())
	_, err := v.Validate(context.Background(), h, "S", "m", []byte(`{"x":2}`))
	fail, ok := err.(*Failure)
	if !ok || fail.Reason != ReasonInvalidSig {
		t.Fatalf("err = %#v", err)
	}
}

func TestValidator_MethodSubstitution(t *testing.T) {
	pub, priv := newKey(t)
	store := NewMemoryStore()
	store.Put("sid", pub)
	now := time.Unix(1_000_000, 0)
	v := New(Options{Store: store, Now: func() time.Time { return now }})

	body := []byte("{}")
	h := headersFor(t, "sid", priv, "S", "read", body, now.Unix())
	_, err := v.Validate(context.Background(), h, "S", "delete", body)
	fail, ok := err.(*Failure)
	if !ok || fail.Reason != ReasonInvalidSig {
		t.Fatalf("err = %#v", err)
	}
}

func TestValidator_BadHexSig(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	v := New(Options{Store: NewMemoryStore(), Now: func() time.Time { return now }})
	h := hdr{"X-Session": "x", "X-Ts": strconv.FormatInt(now.Unix(), 10), "X-Sig": "zz"}
	_, err := v.Validate(context.Background(), h, "S", "m", nil)
	fail, ok := err.(*Failure)
	if !ok || fail.Reason != ReasonBadSignatureFmt {
		t.Fatalf("err = %#v", err)
	}
}
