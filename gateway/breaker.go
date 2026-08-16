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
	// FailureRateThreshold ejects a replica whose failure RATE over the recent
	// window reaches this fraction (0..1), even without consecutive failures —
	// catching a pod that errors intermittently (e.g. 50% 5xx), which the
	// consecutive-count trip alone never notices (W1.6 outlier ejection). 0
	// (default) disables rate-based ejection.
	FailureRateThreshold float64
	// RollingWindow is how many recent outcomes the failure rate is computed
	// over. Default 20 when FailureRateThreshold > 0.
	RollingWindow int
	// MinRequests is the minimum window fill before rate-ejection can trip, so a
	// cold breaker doesn't eject on one or two early errors. Default = window.
	MinRequests int
}

type breakerState int

const (
	breakerClosed breakerState = iota
	breakerOpen
	breakerHalfOpen
)

type upstreamBreaker struct {
	state    breakerState
	failures int // consecutive failures (count-based trip)
	openedAt time.Time

	// Rolling outcome window for rate-based ejection (W1.6). ring[i]=true means
	// that slot was a failure; fails is the live failure count in the ring.
	ring   []bool
	idx    int
	filled int
	fails  int
}

// trip opens the breaker and restarts its cooldown.
func (b *upstreamBreaker) trip(now time.Time) {
	b.state = breakerOpen
	b.openedAt = now
	b.failures = 0 // consecutive counter is a closed-state concern only
}

// observe folds one outcome into the rolling window, evicting the oldest slot
// when full so fails always reflects the last window outcomes.
func (b *upstreamBreaker) observe(failure bool, window int) {
	if len(b.ring) != window {
		b.ring = make([]bool, window)
		b.idx, b.filled, b.fails = 0, 0, 0
	}
	if b.filled == window && b.ring[b.idx] {
		b.fails-- // the slot we're about to overwrite was a failure
	}
	b.ring[b.idx] = failure
	if failure {
		b.fails++
	}
	b.idx = (b.idx + 1) % window
	if b.filled < window {
		b.filled++
	}
}

// resetWindow clears the rolling window — used after a successful recovery probe
// so stale pre-ejection failures don't immediately re-eject the pod.
func (b *upstreamBreaker) resetWindow() {
	for i := range b.ring {
		b.ring[i] = false
	}
	b.idx, b.filled, b.fails = 0, 0, 0
}

// breakerManager holds one breaker per upstream address. Upstream count is
// bounded by the number of pods in the mesh (small, stable), so the map is
// not swept. Safe for concurrent use.
type breakerManager struct {
	disabled  bool
	threshold int
	cooldown  time.Duration
	rate      float64 // rate-ejection threshold (0 = off)
	window    int     // rolling window size
	minReq    int     // min window fill before rate-ejection trips
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
	window := cfg.RollingWindow
	if window <= 0 {
		window = 20
	}
	minReq := cfg.MinRequests
	if minReq <= 0 || minReq > window {
		minReq = window
	}
	return &breakerManager{
		disabled:  cfg.Disabled,
		threshold: threshold,
		cooldown:  cooldown,
		rate:      cfg.FailureRateThreshold,
		window:    window,
		minReq:    minReq,
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

// isOpen reports whether addr's breaker is currently rejecting traffic — used
// by the resolver to skip a tripped replica when picking one. Unlike allow it
// is NON-MUTATING (it never advances an elapsed breaker to half-open): an open
// breaker whose cooldown has elapsed is reported NOT open, so the resolver
// leaves the replica eligible and the dispatch-path allow() owns the probe.
func (m *breakerManager) isOpen(addr string) bool {
	if m.disabled {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.buckets[addr]
	if b == nil {
		return false
	}
	switch b.state {
	case breakerOpen:
		return m.now().Sub(b.openedAt) < m.cooldown
	case breakerHalfOpen:
		return true // a probe is already in flight; steer new traffic elsewhere
	default:
		return false
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
		if ok && m.rate <= 0 {
			return // success on a fresh upstream, no rate tracking: nothing to do
		}
		b = &upstreamBreaker{}
		m.buckets[addr] = b
	}
	if ok {
		wasHalfOpen := b.state == breakerHalfOpen
		b.state = breakerClosed
		b.failures = 0
		if wasHalfOpen {
			// A recovery probe succeeded — start the rolling window fresh so
			// stale pre-ejection failures can't immediately re-eject the pod.
			b.resetWindow()
		}
	} else {
		b.failures++
		if b.state == breakerHalfOpen || b.failures >= m.threshold {
			b.trip(m.now())
		}
	}
	// Rate-based outlier ejection (W1.6): even without a consecutive-failure
	// trip, eject a replica whose failure rate over the window is an outlier.
	if m.rate > 0 {
		b.observe(!ok, m.window)
		if b.state != breakerOpen && b.filled >= m.minReq &&
			float64(b.fails)/float64(b.filled) >= m.rate {
			b.trip(m.now())
		}
	}
}

// snapshot returns each known upstream's current breaker state.
func (m *breakerManager) snapshot() map[string]breakerState {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]breakerState, len(m.buckets))
	for k, b := range m.buckets {
		out[k] = b.state
	}
	return out
}

// BreakerSnapshot returns each known upstream's circuit-breaker state as an int
// (0=closed, 1=open, 2=half-open) for observability — e.g. a metrics gauge or a
// health rollup. An upstream that has never failed is absent (implicitly
// closed).
func (g *Gateway) BreakerSnapshot() map[string]int {
	if g.breakers == nil {
		return nil
	}
	raw := g.breakers.snapshot()
	out := make(map[string]int, len(raw))
	for k, v := range raw {
		out[k] = int(v)
	}
	return out
}

// InFlight is the number of requests currently being handled. Counted at the
// outermost middleware, so it spans the whole dispatch chain.
func (g *Gateway) InFlight() int64 { return g.inFlight.Load() }
