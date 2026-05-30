package proto

import (
	"strings"
	"testing"
	"time"
)

var (
	testSecret = []byte("topsecret-mesh-key")
	testBody   = []byte(`{"name":"Auth","address":"http://auth:9001"}`)
	testNow    = time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
)

func TestRoundTrip(t *testing.T) {
	sig, ts := Sign(testSecret, testBody, testNow)
	if err := Verify(testSecret, sig, ts, testBody, testNow); err != nil {
		t.Fatalf("roundtrip verify: %v", err)
	}
}

func TestTamperedBody(t *testing.T) {
	sig, ts := Sign(testSecret, testBody, testNow)
	mutated := []byte(`{"name":"Evil","address":"http://attacker:9999"}`)
	err := Verify(testSecret, sig, ts, mutated, testNow)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("tampered body should fail, got %v", err)
	}
}

func TestExpiredTimestamp(t *testing.T) {
	signedAt := testNow.Add(-10 * time.Minute)
	sig, ts := Sign(testSecret, testBody, signedAt)
	err := Verify(testSecret, sig, ts, testBody, testNow)
	if err == nil || !strings.Contains(err.Error(), "skew window") {
		t.Fatalf("expired ts should fail, got %v", err)
	}
}

func TestFutureTimestamp(t *testing.T) {
	signedAt := testNow.Add(10 * time.Minute)
	sig, ts := Sign(testSecret, testBody, signedAt)
	err := Verify(testSecret, sig, ts, testBody, testNow)
	if err == nil || !strings.Contains(err.Error(), "skew window") {
		t.Fatalf("future ts should fail, got %v", err)
	}
}

func TestWrongSecret(t *testing.T) {
	sig, ts := Sign(testSecret, testBody, testNow)
	err := Verify([]byte("other-secret"), sig, ts, testBody, testNow)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("wrong secret should fail, got %v", err)
	}
}

func TestMissingHeaders(t *testing.T) {
	err := Verify(testSecret, "", "", testBody, testNow)
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing headers should fail, got %v", err)
	}
}

func TestBadHex(t *testing.T) {
	_, ts := Sign(testSecret, testBody, testNow)
	err := Verify(testSecret, "not-hex-zzz", ts, testBody, testNow)
	if err == nil || !strings.Contains(err.Error(), "hex") {
		t.Fatalf("bad hex should fail, got %v", err)
	}
}

func TestBadTimestamp(t *testing.T) {
	err := Verify(testSecret, "deadbeef", "not-a-number", testBody, testNow)
	if err == nil || !strings.Contains(err.Error(), "unix timestamp") {
		t.Fatalf("bad ts should fail, got %v", err)
	}
}
