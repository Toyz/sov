package gateway_test

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	. "github.com/Toyz/sov/gateway"
)

// spyCache is a ClaimsCache that records Get/Put so the test can prove the
// custom cache (via WithClaimsCache) is actually consulted.
type spyCache struct {
	mu         sync.Mutex
	m          map[string]*Claims
	gets, puts int
}

func (s *spyCache) Get(token string) (*Claims, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gets++
	cl, ok := s.m[token]
	if !ok {
		return nil, false
	}
	if !cl.ExpiresAt.IsZero() && time.Now().UTC().After(cl.ExpiresAt) {
		delete(s.m, token)
		return nil, false
	}
	return cl, true
}

func (s *spyCache) Put(token string, cl *Claims) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.puts++
	s.m[token] = cl
}

func TestClaimsCache_CustomIsConsulted(t *testing.T) {
	spy := &spyCache{m: map[string]*Claims{}}
	gw := New(WithClaimsCache(spy))
	gw.RegisterAuth(&AuthRouter{})
	gw.Register(&WhoRouter{})

	req := &Request{Method: http.MethodPost, Path: "/rpc/Who/me", Header: Header{"Authorization": "Bearer good-bob"}}
	for i := 0; i < 3; i++ {
		if resp := gw.Handle(context.Background(), req); resp.Status != 200 {
			t.Fatalf("iter %d: status=%d body=%s", i, resp.Status, resp.Body)
		}
	}

	spy.mu.Lock()
	gets, puts := spy.gets, spy.puts
	spy.mu.Unlock()
	if gets < 3 {
		t.Errorf("custom cache Get called %d times, want >=3 (once per request)", gets)
	}
	if puts != 1 {
		t.Errorf("custom cache Put called %d times, want 1 (only the first miss)", puts)
	}
}
