package gateway_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	. "github.com/Toyz/sov/gateway"
)

// registerBodyAddr builds a register body with an explicit address (the shared
// registerBody helper derives the address from the name, so it can't express
// two replicas of one service).
func registerBodyAddr(name, address string) []byte {
	out, _ := json.Marshal(map[string]any{
		"name": name, "address": address, "heartbeat_interval_seconds": 10,
	})
	return out
}

// Two addresses under one service name are REPLICAS (W1.1): the resolver
// round-robins across them so load spreads and either can serve.
func TestReplicas_RoundRobinAcrossAddresses(t *testing.T) {
	r := NewRegisterResolver(time.Hour)
	defer r.Close()
	r.Put("Svc", "http://a:9000", time.Hour)
	r.Put("Svc", "http://b:9000", time.Hour)

	seen := map[string]int{}
	for i := 0; i < 10; i++ {
		ep, ok := r.Resolve(context.Background(), "Svc")
		if !ok {
			t.Fatal("Svc should resolve with two replicas registered")
		}
		seen[ep.RemoteAddr]++
	}
	if seen["http://a:9000"] == 0 || seen["http://b:9000"] == 0 {
		t.Fatalf("round-robin should hit BOTH replicas, got %v", seen)
	}
}

// End-to-end over the signed /rpc/_register path: two pods registering the SAME
// service name at different addresses both become live replicas. Before W1.1 the
// second registration was rejected 409 SERVICE_CONFLICT; now it's HA.
func TestReplicas_TwoPodsRegisterSameServiceViaHTTP(t *testing.T) {
	secret := []byte("topsecret")
	gw := useMeshSecret(t, secret)

	postSignedRegister(t, gw, secret, registerBodyAddr("Feed", "http://feed-1:9000"))
	postSignedRegister(t, gw, secret, registerBodyAddr("Feed", "http://feed-2:9000"))

	seen := map[string]int{}
	for i := 0; i < 12; i++ {
		ep, ok := gw.Resolver().Resolve(context.Background(), "Feed")
		if !ok {
			t.Fatal("Feed should resolve after two pods registered")
		}
		seen[ep.RemoteAddr]++
	}
	if len(seen) != 2 {
		t.Fatalf("both pods must register as replicas (no 409); saw %v", seen)
	}
}

// Deregistering one replica leaves the survivor serving; deleting the last one
// removes the service.
func TestReplicas_DeleteOneLeavesSurvivor(t *testing.T) {
	r := NewRegisterResolver(time.Hour)
	defer r.Close()
	r.Put("Svc", "http://a:9000", time.Hour)
	r.Put("Svc", "http://b:9000", time.Hour)

	r.Delete("Svc", "http://a:9000")
	for i := 0; i < 6; i++ {
		ep, ok := r.Resolve(context.Background(), "Svc")
		if !ok {
			t.Fatal("Svc should still resolve via the surviving replica")
		}
		if ep.RemoteAddr != "http://b:9000" {
			t.Fatalf("only b should remain, got %s", ep.RemoteAddr)
		}
	}

	r.Delete("Svc", "http://b:9000")
	if _, ok := r.Resolve(context.Background(), "Svc"); ok {
		t.Fatal("Svc should be gone once all replicas are deregistered")
	}
}

// Re-registering the SAME address refreshes in place rather than adding a
// duplicate replica.
func TestReplicas_SameAddressRefreshesNotDuplicates(t *testing.T) {
	r := NewRegisterResolver(time.Hour)
	defer r.Close()
	r.Put("Svc", "http://a:9000", time.Hour)
	r.Put("Svc", "http://a:9000", time.Hour)

	// Only one live address should exist — deleting it removes the service.
	r.Delete("Svc", "http://a:9000")
	if _, ok := r.Resolve(context.Background(), "Svc"); ok {
		t.Fatal("a single address re-put twice must collapse to one replica")
	}
}
