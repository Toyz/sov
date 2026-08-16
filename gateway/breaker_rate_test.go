package gateway

import (
	"testing"
	"time"
)

// A replica that fails intermittently (50%) never hits 5 consecutive failures,
// so the count-based trip never fires — but rate-based ejection (W1.6) catches
// it and opens the breaker so the picker steers traffic to healthier replicas.
func TestBreaker_RateEjectsIntermittentReplica(t *testing.T) {
	m := newBreakerManager(BreakerConfig{
		FailureThreshold:     5, // never 5-in-a-row below, so this won't trip
		FailureRateThreshold: 0.5,
		RollingWindow:        10,
	})
	addr := "http://flaky:9000"
	// Alternating F,O,F,O,... = 50% failures, max 1 consecutive.
	for i := 0; i < 10; i++ {
		m.record(addr, i%2 == 1) // even i -> failure, odd i -> success
	}
	if m.allow(addr) {
		t.Fatal("a replica at the failure-rate threshold should be ejected (breaker open)")
	}
}

// Same intermittent pattern, but with rate ejection disabled (default), the
// replica stays in rotation — proving it's the rate config doing the ejection,
// not the consecutive-count path.
func TestBreaker_NoRateConfigKeepsIntermittentReplica(t *testing.T) {
	m := newBreakerManager(BreakerConfig{FailureThreshold: 5}) // no FailureRateThreshold
	addr := "http://flaky:9000"
	for i := 0; i < 20; i++ {
		m.record(addr, i%2 == 1)
	}
	if !m.allow(addr) {
		t.Fatal("without rate ejection an intermittent replica must stay in rotation")
	}
}

// After ejection and cooldown, a successful recovery probe clears the rolling
// window so the pod isn't immediately re-ejected by its stale failure history.
func TestBreaker_RateResetsWindowOnRecovery(t *testing.T) {
	now := time.Now()
	m := newBreakerManager(BreakerConfig{
		FailureThreshold:     100, // keep the count path out of the way
		FailureRateThreshold: 0.5,
		RollingWindow:        10,
		Cooldown:             time.Second,
	})
	m.now = func() time.Time { return now }
	addr := "http://flaky:9000"
	for i := 0; i < 10; i++ {
		m.record(addr, i%2 == 1)
	}
	if m.allow(addr) {
		t.Fatal("should be ejected after hitting the rate threshold")
	}
	// Cooldown elapses -> allow lets one probe through (half-open).
	now = now.Add(2 * time.Second)
	if !m.allow(addr) {
		t.Fatal("after cooldown a probe should be allowed (half-open)")
	}
	// Probe succeeds -> window reset. A single subsequent failure must not
	// re-eject (stale history is gone).
	m.record(addr, true)
	m.record(addr, false)
	if !m.allow(addr) {
		t.Fatal("one failure after recovery must not re-eject (window was reset)")
	}
}
