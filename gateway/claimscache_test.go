package gateway

import (
	"testing"
	"time"
)

// A non-expiring (or long-lived) token must be re-verified at least every ttl,
// so revocation can't be defeated by caching.
func TestClaimsCache_MaxAgeForcesReverify(t *testing.T) {
	now := time.Unix(1_000_000, 0).UTC()
	c := newMemClaimsCache(60 * time.Second)
	c.now = func() time.Time { return now }

	c.Put("tok", &Claims{Subject: "u"}) // zero ExpiresAt: never expires on its own
	if _, ok := c.Get("tok"); !ok {
		t.Fatal("fresh entry should hit")
	}
	now = now.Add(30 * time.Second)
	if _, ok := c.Get("tok"); !ok {
		t.Fatal("within ttl should still hit")
	}
	now = now.Add(31 * time.Second) // 61s > 60s ttl
	if _, ok := c.Get("tok"); ok {
		t.Fatal("past max-age must miss and force a re-verify, even for a non-expiring token")
	}
}

func TestClaimsCache_HonorsExpiresAt(t *testing.T) {
	now := time.Unix(1_000_000, 0).UTC()
	c := newMemClaimsCache(time.Hour) // long max-age; ExpiresAt is the tighter bound
	c.now = func() time.Time { return now }

	c.Put("tok", &Claims{Subject: "u", ExpiresAt: now.Add(10 * time.Second)})
	if _, ok := c.Get("tok"); !ok {
		t.Fatal("before expiry should hit")
	}
	now = now.Add(11 * time.Second)
	if _, ok := c.Get("tok"); ok {
		t.Fatal("after ExpiresAt should miss")
	}
}
