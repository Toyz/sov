package gateway

import (
	"context"
	"testing"
	"time"
)

// A replica whose breaker is open is skipped when the resolver picks — traffic
// steers to the healthy replica. When ALL replicas are open the resolver falls
// back to the full set rather than stranding the service, so half-open recovery
// can still probe. (Internal test: it sets the unexported breakerOpen hook the
// Gateway wires from its breakerManager.)
func TestReplicas_SkipsOpenBreakerWithAllOpenFallback(t *testing.T) {
	r := NewRegisterResolver(time.Hour)
	defer r.Close()
	r.Put("Svc", "http://good:9000", time.Hour)
	r.Put("Svc", "http://bad:9000", time.Hour)

	r.breakerOpen = func(addr string) bool { return addr == "http://bad:9000" }
	for i := 0; i < 8; i++ {
		ep, ok := r.Resolve(context.Background(), "Svc")
		if !ok {
			t.Fatal("healthy replica should always resolve")
		}
		if ep.RemoteAddr != "http://good:9000" {
			t.Fatalf("open-breaker replica must be skipped, got %s", ep.RemoteAddr)
		}
	}

	// Every replica open → do not strand; still hand one out for the probe.
	r.breakerOpen = func(addr string) bool { return true }
	if _, ok := r.Resolve(context.Background(), "Svc"); !ok {
		t.Fatal("all-open must fall back to the full set, not return no endpoint")
	}
}
