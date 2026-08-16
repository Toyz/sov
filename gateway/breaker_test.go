package gateway

import (
	"testing"
	"time"
)

func TestBreaker_OpensThenHalfOpensThenCloses(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	m := newBreakerManager(BreakerConfig{FailureThreshold: 3, Cooldown: 10 * time.Second})
	m.now = func() time.Time { return now }
	addr := "http://pod:9000"

	if !m.allow(addr) {
		t.Fatal("fresh upstream must be allowed")
	}
	m.record(addr, false)
	m.record(addr, false)
	if !m.allow(addr) {
		t.Fatal("2 failures < threshold: still closed")
	}
	m.record(addr, false) // 3rd consecutive failure trips open
	if m.allow(addr) {
		t.Fatal("threshold reached: breaker must be open (fail fast)")
	}

	now = now.Add(9 * time.Second)
	if m.allow(addr) {
		t.Fatal("still within cooldown: keep failing fast")
	}
	now = now.Add(2 * time.Second) // 11s > cooldown
	if !m.allow(addr) {
		t.Fatal("cooldown elapsed: one half-open probe must be allowed")
	}
	if m.allow(addr) {
		t.Fatal("half-open allows only ONE probe; others fail fast")
	}

	m.record(addr, false) // probe fails -> reopen
	if m.allow(addr) {
		t.Fatal("failed probe must reopen the breaker")
	}
	now = now.Add(11 * time.Second)
	if !m.allow(addr) {
		t.Fatal("probe allowed after second cooldown")
	}
	m.record(addr, true) // probe succeeds -> closed
	if !m.allow(addr) || !m.allow(addr) {
		t.Fatal("successful probe must close the breaker")
	}
}

func TestBreaker_SuccessResetsFailureRun(t *testing.T) {
	m := newBreakerManager(BreakerConfig{FailureThreshold: 3})
	addr := "http://pod:9000"
	m.record(addr, false)
	m.record(addr, false)
	m.record(addr, true) // clears the run
	m.record(addr, false)
	m.record(addr, false)
	if !m.allow(addr) {
		t.Fatal("only consecutive failures count; 2 < 3 after a success reset")
	}
}

func TestBreaker_IsolatesUpstreams(t *testing.T) {
	m := newBreakerManager(BreakerConfig{FailureThreshold: 2})
	a, b := "http://a:9000", "http://b:9000"
	m.record(a, false)
	m.record(a, false) // a opens
	if m.allow(a) {
		t.Fatal("a should be open")
	}
	if !m.allow(b) {
		t.Fatal("b is a distinct upstream and must be unaffected")
	}
}

func TestBreaker_DisabledAlwaysAllows(t *testing.T) {
	m := newBreakerManager(BreakerConfig{Disabled: true, FailureThreshold: 1})
	addr := "http://pod:9000"
	m.record(addr, false)
	m.record(addr, false)
	if !m.allow(addr) {
		t.Fatal("disabled breaker must always allow")
	}
}

func TestBreaker_Defaults(t *testing.T) {
	m := newBreakerManager(BreakerConfig{})
	if m.threshold != 5 || m.cooldown != 10*time.Second {
		t.Fatalf("defaults: threshold=%d cooldown=%s", m.threshold, m.cooldown)
	}
}
