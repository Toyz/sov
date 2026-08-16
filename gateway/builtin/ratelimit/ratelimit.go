// Package ratelimit is an optional rate limiter on the HeaderParser seam.
// It counts inbound business calls per key — the authenticated subject
// when present, else the caller's source IP — and rejects a caller over
// its budget with 429 RATE_LIMITED before the call reaches a router.
//
// The decision backend is pluggable via the Limiter interface. The
// default is an in-memory token bucket (single-process); a deployment
// that runs several gateway replicas and wants ONE shared budget supplies
// its own Limiter backed by a shared store (Redis, a DB, a sidecar) —
// the plugin does not care how the decision is made. This is the
// reference fill for the rate-limit seam in docs/SECURITY.md: Sov ships
// no limiter by default (rate policy is a deployment concern), and even
// the storage of the counters is a seam the consumer can own.
//
//	// default in-memory bucket
//	gw.Use(ratelimit.New(ratelimit.Config{RequestsPerSecond: 20, Burst: 40}))
//
//	// bring your own (e.g. Redis-backed)
//	gw.Use(ratelimit.New(ratelimit.Config{Limiter: myRedisLimiter}))
//
// Ordering: as a HeaderParser it runs after the auth middleware resolves
// the bearer (so subject keying works) and before dispatch. It does NOT
// run ahead of bearer verification, so it is a quota limiter, not an
// auth-brute-force shield — put that at your edge proxy. Framework paths
// (/rpc/_health, /rpc/_introspect, /rpc/_register, /rpc/_batch) are
// exempt so probes and mesh heartbeats are never throttled; the calls a
// batch fans out are governed by the batch plugin's own entry cap.
package ratelimit

import (
	"strings"
	"sync"
	"time"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/rpc"
)

// Limiter is the pluggable decision backend. Implementations must be safe
// for concurrent use. Allow reports whether a call keyed by key may
// proceed now, consuming one unit of that key's budget when it returns
// true. A distributed implementation that cannot reach its backing store
// chooses its own fail-open / fail-closed policy. Set Config.Limiter to
// swap the default in-memory TokenBucket for your own.
type Limiter interface {
	Allow(key string) bool
}

// Config configures the plugin. Supply EITHER a rate (RequestsPerSecond,
// optionally Burst) to use the default in-memory TokenBucket, OR a custom
// Limiter. A non-nil Limiter wins and the rate fields are ignored.
type Config struct {
	// RequestsPerSecond is the sustained per-key refill rate for the
	// default bucket. Must be > 0 when no Limiter is supplied.
	RequestsPerSecond float64
	// Burst is the default bucket capacity (max back-to-back calls after
	// idling). Defaults to ceil(RequestsPerSecond), minimum 1.
	Burst int
	// Limiter, if set, replaces the default in-memory bucket entirely.
	Limiter Limiter
}

// Plugin is the rate-limit HeaderParser returned by New.
type Plugin struct {
	limiter Limiter
}

// New returns a ratelimit plugin from cfg. With no custom Limiter it
// builds the default in-memory TokenBucket and panics if RequestsPerSecond
// is not > 0 — a limiter with neither a rate nor a backend is a config
// error, caught at construction rather than silently passing everything.
func New(cfgs ...Config) *Plugin {
	if len(cfgs) > 1 {
		panic("ratelimit.New: at most one Config")
	}
	var cfg Config
	if len(cfgs) == 1 {
		cfg = cfgs[0]
	}
	lim := cfg.Limiter
	if lim == nil {
		if cfg.RequestsPerSecond <= 0 {
			panic("ratelimit.New: RequestsPerSecond must be > 0 (or supply a Limiter)")
		}
		lim = NewTokenBucket(cfg.RequestsPerSecond, cfg.Burst)
	}
	return &Plugin{limiter: lim}
}

// Compile-time proof of the hooks this plugin binds — a signature
// drift here is a build error, not a silent non-binding at runtime.
var (
	_ gateway.Plugin       = (*Plugin)(nil)
	_ gateway.PluginDoc    = (*Plugin)(nil)
	_ gateway.HeaderParser = (*Plugin)(nil)
)

// PluginName surfaces in /rpc/_introspect.plugins[].
func (p *Plugin) PluginName() string { return "ratelimit" }

// Doc surfaces a one-line description in /rpc/_introspect + the explorer.
func (p *Plugin) Doc() string {
	return "Token-bucket rate limiter: rejects a subject/IP over its rate with 429 before dispatch."
}

// ParseHeaders enforces the limiter for one inbound business call.
func (p *Plugin) ParseHeaders(req *gateway.Request) *rpc.Error {
	// Framework paths (health probes, introspect, mesh register/heartbeat,
	// the batch envelope) are never throttled.
	if strings.HasPrefix(req.Path, "/rpc/_") {
		return nil
	}
	if p.limiter.Allow(key(req)) {
		return nil
	}
	return rpc.TooManyRequests("rate limit exceeded")
}

// key is the bucket key: the authenticated subject if present, else the
// source IP, else a shared anonymous bucket. Prefixed so a subject can
// never alias an IP.
func key(req *gateway.Request) string {
	switch u := req.User.(type) {
	case string:
		if u != "" {
			return "sub:" + u
		}
	case *gateway.Claims:
		if u != nil && u.Subject != "" {
			return "sub:" + u.Subject
		}
	}
	if req.RemoteIP != "" {
		return "ip:" + req.RemoteIP
	}
	return "anon"
}

// bucket is one key's token state.
type bucket struct {
	tokens float64
	last   time.Time
}

// TokenBucket is the default in-memory Limiter: one refilling bucket per
// key, capacity burst, refilling at rate tokens/sec. The bucket map is
// bounded by a lazy sweep that drops buckets idle long enough to have
// refilled to full (indistinguishable from a fresh one). Safe for
// concurrent use. Single-process only — for one budget across replicas,
// implement Limiter over a shared store instead.
type TokenBucket struct {
	rate  float64
	burst float64
	now   func() time.Time

	mu        sync.Mutex
	buckets   map[string]*bucket
	idleTTL   time.Duration
	lastSweep time.Time
}

var _ Limiter = (*TokenBucket)(nil)

// NewTokenBucket builds the default limiter. requestsPerSecond must be > 0;
// burst defaults to ceil(requestsPerSecond), minimum 1, when <= 0.
func NewTokenBucket(requestsPerSecond float64, burst int) *TokenBucket {
	if requestsPerSecond <= 0 {
		panic("ratelimit.NewTokenBucket: requestsPerSecond must be > 0")
	}
	b := float64(burst)
	if b <= 0 {
		b = float64(int(requestsPerSecond))
		if b < 1 {
			b = 1
		}
	}
	// A bucket idle this long has refilled from empty to full, so it is
	// indistinguishable from a fresh one and safe to evict.
	idle := time.Duration(b/requestsPerSecond*float64(time.Second)) + time.Second
	return &TokenBucket{
		rate:    requestsPerSecond,
		burst:   b,
		now:     time.Now,
		buckets: map[string]*bucket{},
		idleTTL: idle,
	}
}

// Allow debits one token from key's bucket, refilling by elapsed time
// first. Returns false when the bucket is empty.
func (t *TokenBucket) Allow(key string) bool {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()

	// Bounded lazy sweep: at most once per idle window, drop buckets that
	// have refilled to full so the map cannot grow without limit.
	if now.Sub(t.lastSweep) >= t.idleTTL {
		for k, b := range t.buckets {
			if now.Sub(b.last) >= t.idleTTL {
				delete(t.buckets, k)
			}
		}
		t.lastSweep = now
	}

	b := t.buckets[key]
	if b == nil {
		// A fresh key starts full, then spends one token for this call.
		t.buckets[key] = &bucket{tokens: t.burst - 1, last: now}
		return true
	}
	elapsed := now.Sub(b.last).Seconds()
	b.tokens += elapsed * t.rate
	if b.tokens > t.burst {
		b.tokens = t.burst
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}
