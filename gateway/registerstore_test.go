package gateway_test

import (
	"context"
	"sync"
	"testing"
	"time"

	. "github.com/Toyz/sov/gateway"
)

// sharedStore is a RegisterStore two resolvers can point at — modeling a
// shared (e.g. Redis) backend behind multiple gateway replicas.
type sharedStore struct {
	mu sync.Mutex
	m  map[string]RegisterEntry
}

func newSharedStore() *sharedStore { return &sharedStore{m: map[string]RegisterEntry{}} }

func (s *sharedStore) Put(svc string, e RegisterEntry) {
	s.mu.Lock()
	s.m[svc] = e
	s.mu.Unlock()
}
func (s *sharedStore) Delete(svc string) {
	s.mu.Lock()
	delete(s.m, svc)
	s.mu.Unlock()
}
func (s *sharedStore) Snapshot() map[string]RegisterEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]RegisterEntry, len(s.m))
	for k, v := range s.m {
		out[k] = v
	}
	return out
}
func (s *sharedStore) ReapExpired(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for k, e := range s.m {
		if now.After(e.ExpiresAt) {
			delete(s.m, k)
			changed = true
		}
	}
	return changed
}

// Replica A registers a pod; replica B (sharing the store, polling via
// WithRegisterRefresh) must converge to see it — the multi-replica fix.
func TestRegisterStore_SharedConvergence(t *testing.T) {
	store := newSharedStore()
	a := NewRegisterResolver(time.Hour, WithRegisterStore(store))
	b := NewRegisterResolver(time.Hour, WithRegisterStore(store), WithRegisterRefresh(15*time.Millisecond))
	defer a.Close()
	defer b.Close()

	a.PutEntry("Foo", "http://foo:9000", time.Hour, EntryOptions{Introspectable: true})

	// A (the replica that took the registration) sees it immediately.
	if _, ok := a.Resolve(context.Background(), "Foo"); !ok {
		t.Fatal("replica A should resolve its own registration immediately")
	}
	// B converges via refresh polling.
	if !eventually(t, 2*time.Second, func() bool {
		_, ok := b.Resolve(context.Background(), "Foo")
		return ok
	}) {
		t.Fatal("replica B never converged to the shared registration")
	}

	// Delete on A propagates to B too.
	a.Delete("Foo")
	if !eventually(t, 2*time.Second, func() bool {
		_, ok := b.Resolve(context.Background(), "Foo")
		return !ok
	}) {
		t.Fatal("replica B never saw the deletion")
	}
}

func TestRegisterStore_CustomStoreIsWritten(t *testing.T) {
	store := newSharedStore()
	r := NewRegisterResolver(time.Hour, WithRegisterStore(store))
	defer r.Close()
	r.Put("Bar", "http://bar:9000", time.Hour)
	if _, ok := store.m["Bar"]; !ok {
		t.Fatal("PutEntry must write through to the custom store")
	}
}

func eventually(t *testing.T, d time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}
