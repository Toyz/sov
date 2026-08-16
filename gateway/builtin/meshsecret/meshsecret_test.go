package meshsecret

import (
	"strconv"
	"testing"
	"time"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/meshsecret/proto"
)

func signedReq(secret, body []byte, now time.Time) *gateway.Request {
	sig, ts := proto.Sign(secret, body, now)
	return &gateway.Request{
		Path: "/rpc/_register",
		Header: gateway.Header{
			proto.RegisterSigHeader: sig,
			proto.RegisterTsHeader:  ts,
		},
		Body: body,
	}
}

func TestParseHeaders_RejectsReplay(t *testing.T) {
	secret := []byte("topsecret")
	p := New(Config{Secret: secret})
	body := []byte(`{"name":"Feed"}`)
	now := time.Now()

	if err := p.ParseHeaders(signedReq(secret, body, now)); err != nil {
		t.Fatalf("first register should pass: %v", err)
	}
	// Byte-identical resend (same sig+ts+body) is a replay → reject.
	if err := p.ParseHeaders(signedReq(secret, body, now)); err == nil {
		t.Fatal("replay of an identical signature should be rejected")
	}
}

func TestParseHeaders_FreshTimestampPasses(t *testing.T) {
	secret := []byte("topsecret")
	p := New(Config{Secret: secret})
	body := []byte(`{"name":"Feed"}`)

	// Two beats a second apart (distinct ts → distinct sig) both pass — a
	// heartbeat re-sign must never trip the replay guard.
	if err := p.ParseHeaders(signedReq(secret, body, time.Now())); err != nil {
		t.Fatalf("first beat: %v", err)
	}
	if err := p.ParseHeaders(signedReq(secret, body, time.Now().Add(time.Second))); err != nil {
		t.Fatalf("second beat with fresh ts should pass: %v", err)
	}
}

func TestParseHeaders_EmptySecretDisabled(t *testing.T) {
	p := New(Config{}) // no secret → gate + replay guard disabled
	req := &gateway.Request{Path: "/rpc/_register", Header: gateway.Header{}, Body: []byte(`{}`)}
	if err := p.ParseHeaders(req); err != nil {
		t.Fatalf("no-secret gateway should pass through: %v", err)
	}
}

func TestMarkSeen_SweepEvictsExpired(t *testing.T) {
	p := New(Config{Secret: []byte("x")})
	now := time.Now()
	// Seed an entry whose expiry is already in the past.
	p.seen["stale"] = now.Add(-time.Minute).UTC().Unix()
	if err := p.markSeen("fresh", strconv.FormatInt(now.UTC().Unix(), 10), now); err != nil {
		t.Fatalf("markSeen fresh: %v", err)
	}
	if _, ok := p.seen["stale"]; ok {
		t.Fatal("expired sig should have been swept")
	}
	if _, ok := p.seen["fresh"]; !ok {
		t.Fatal("fresh sig should be retained")
	}
}
