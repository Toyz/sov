package gateway

import (
	"sync"
	"time"
)

// BreakerConfig tunes the per-upstream circuit breaker that guards remote
// dispatch. Zero values take the defaults below.
type BreakerConfig struct {
	// FailureThreshold is the number of consecutive transport failures to
	// one upstream that trips its breaker open. Default 5.
	FailureThreshold int
	// Cooldown is how long a tripped breaker fails fast before allowing a
	// single probe (half-open). Default 10s.
	Cooldown time.Duration
	// Disabled turns the breaker off entirely — every remote dispatch is
	// attempted, as it was before the breaker existed.
	Disabled bool
}

type breakerState int

const (
	breakerClosed breakerState = iota
	breakerOpen
	breakerHalfOpen
)

type upstreamBreaker struct {
	state    breakerState
	failures int
	openedAt time.Time
}

// breakerManager holds one breaker per upstream address. Upstream count is
// bounded by the number of pods in the mesh (small, stable), so the map is
// not swept. Safe for concurrent use.
type breakerManager struct {
	disabled  bool
	threshold int
	cooldown  time.Duration
	now       func() time.Time

	mu      sync.Mutex
	buckets map[string]*upstreamBreaker
}

func newBreakerManager(cfg BreakerConfig) *breakerManager {
	threshold := cfg.FailureThreshold
	if threshold <= 0 {
		threshold = 5
	}
	cooldown := cfg.Cooldown
	if cooldown <= 0 {
		cooldown = 10 * time.Second
	}
	return &breakerManager{
		disabled:  cfg.Disabled,
		threshold: threshold,
		cooldown:  cooldown,
		now:       time.Now,
		buckets:   map[string]*upstreamBreaker{},
	}
}

// allow reports whether a call to addr may proceed. An open breaker fails
// fast until its cooldown elapses, after which it lets exactly one probe
// through (half-open) while every other caller keeps failing fast.
func (m *breakerManager) allow(addr string) bool {
	if m.disabled {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.buckets[addr]
	if b == nil {
		return true // never-seen upstream is closed
	}
	switch b.state {
	case breakerOpen:
		if m.now().Sub(b.openedAt) >= m.cooldown {
			b.state = breakerHalfOpen
			return true // single probe
		}
		return false
	case breakerHalfOpen:
		return false // a probe is already in flight
	default:
		return true
	}
}

// record folds the outcome of a remote call into addr's breaker. A success
// closes it and clears the failure count; a failure increments it and trips
// the breaker open at the threshold (or immediately when a half-open probe
// fails).
func (m *breakerManager) record(addr string, ok bool) {
	if m.disabled {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.buckets[addr]
	if b == nil {
		if ok {
			return // success on a fresh upstream: nothing to track yet
		}
		b = &upstreamBreaker{}
		m.buckets[addr] = b
	}
	if ok {
		b.state = breakerClosed
		b.failures = 0
		return
	}
	b.failures++
	if b.state == breakerHalfOpen || b.failures >= m.threshold {
		b.state = breakerOpen
		b.openedAt = m.now()
		b.failures = 0 // reopening restarts the cooldown; counter is closed-state only
	}
}
