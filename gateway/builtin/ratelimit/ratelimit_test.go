package ratelimit

import (
	"testing"
	"time"

	"github.com/Toyz/sov/gateway"
)

// clocked builds a plugin backed by a TokenBucket whose clock is driven
// by *cur, and returns both so tests can inspect bucket state.
func clocked(t *testing.T, rps float64, burst int, cur *time.Time) (*Plugin, *TokenBucket) {
	t.Helper()
	tb := NewTokenBucket(rps, burst)
	tb.now = func() time.Time { return *cur }
	return New(Config{Limiter: tb}), tb
}

func req(path, subject, ip string) *gateway.Request {
	r := &gateway.Request{Path: path, RemoteIP: ip}
	if subject != "" {
		r.User = subject
	}
	return r
}

func TestRateLimit_BurstThenReject(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	p, _ := clocked(t, 10, 2, &now)

	if err := p.ParseHeaders(req("/rpc/Chirp/list", "alice", "")); err != nil {
		t.Fatalf("call 1 should pass: %v", err)
	}
	if err := p.ParseHeaders(req("/rpc/Chirp/list", "alice", "")); err != nil {
		t.Fatalf("call 2 (burst) should pass: %v", err)
	}
	if err := p.ParseHeaders(req("/rpc/Chirp/list", "alice", "")); err == nil {
		t.Fatal("call 3 should be rejected (429)")
	} else if err.Status != 429 {
		t.Fatalf("want 429, got %d", err.Status)
	}
}

func TestRateLimit_RefillsOverTime(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	p, _ := clocked(t, 10, 1, &now)

	if err := p.ParseHeaders(req("/rpc/Chirp/list", "alice", "")); err != nil {
		t.Fatalf("first call should pass: %v", err)
	}
	if err := p.ParseHeaders(req("/rpc/Chirp/list", "alice", "")); err == nil {
		t.Fatal("second immediate call should be rejected")
	}
	now = now.Add(100 * time.Millisecond) // 10 rps -> 1 token / 100ms
	if err := p.ParseHeaders(req("/rpc/Chirp/list", "alice", "")); err != nil {
		t.Fatalf("call after refill should pass: %v", err)
	}
}

func TestRateLimit_PerKeyIsolation(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	p, _ := clocked(t, 1, 1, &now)

	if err := p.ParseHeaders(req("/rpc/Chirp/list", "alice", "")); err != nil {
		t.Fatalf("alice call should pass: %v", err)
	}
	if err := p.ParseHeaders(req("/rpc/Chirp/list", "alice", "")); err == nil {
		t.Fatal("alice second call should be rejected")
	}
	if err := p.ParseHeaders(req("/rpc/Chirp/list", "bob", "")); err != nil {
		t.Fatalf("bob should have his own bucket: %v", err)
	}
	if err := p.ParseHeaders(req("/rpc/Chirp/list", "", "10.0.0.9")); err != nil {
		t.Fatalf("ip-keyed caller should have its own bucket: %v", err)
	}
}

func TestRateLimit_FrameworkPathsExempt(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	p, _ := clocked(t, 1, 1, &now)
	for i := 0; i < 50; i++ {
		if err := p.ParseHeaders(req("/rpc/_health", "", "10.0.0.1")); err != nil {
			t.Fatalf("framework path must be exempt, got %v at i=%d", err, i)
		}
	}
}

func TestRateLimit_SweepEvictsIdleBuckets(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	p, tb := clocked(t, 10, 2, &now)
	p.ParseHeaders(req("/rpc/Chirp/list", "alice", ""))
	if len(tb.buckets) != 1 {
		t.Fatalf("expected 1 bucket, got %d", len(tb.buckets))
	}
	now = now.Add(tb.idleTTL + time.Second)
	p.ParseHeaders(req("/rpc/Chirp/list", "bob", ""))
	if _, ok := tb.buckets["sub:alice"]; ok {
		t.Fatal("idle bucket for alice should have been swept")
	}
}

// denyAll is a custom Limiter proving Config.Limiter is honored.
type denyAll struct{ calls int }

func (d *denyAll) Allow(string) bool { d.calls++; return false }

func TestRateLimit_CustomLimiterHonored(t *testing.T) {
	d := &denyAll{}
	p := New(Config{Limiter: d})
	if err := p.ParseHeaders(req("/rpc/Chirp/list", "alice", "")); err == nil {
		t.Fatal("custom deny-all limiter should reject")
	}
	if d.calls != 1 {
		t.Fatalf("custom limiter should be called once, got %d", d.calls)
	}
	// Framework paths bypass the limiter entirely — no Allow call.
	p.ParseHeaders(req("/rpc/_health", "alice", ""))
	if d.calls != 1 {
		t.Fatalf("framework path must not consult the limiter, calls=%d", d.calls)
	}
}

func TestRateLimit_NewPanicsWithoutRateOrLimiter(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("New with no rate and no Limiter should panic")
		}
	}()
	New(Config{RequestsPerSecond: 0})
}
