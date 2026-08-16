// Package idempotency lets a client make a mutating call safe to retry: it
// sends an Idempotency-Key header, and a replay of the same key returns the
// original SUCCESSFUL response instead of executing the handler again. Since
// every sov method is POST, a network/breaker retry could otherwise
// double-execute a write; this closes that.
//
//	gw.Use(idempotency.New())                                   // in-memory, 10m
//	gw.Use(idempotency.New(idempotency.Config{Store: myRedis})) // shared/persistent
//
// The Store is pluggable (in-memory default); supply your own to share replay
// state across gateway replicas or persist it. Only 2xx responses are cached —
// a failed attempt is re-tried, not replayed — and streaming responses are
// never cached. Best-effort: two concurrent first-attempts with the same key
// can both execute; a persistent Store can add locking if you need strict
// once-only semantics.
package idempotency

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/Toyz/sov/gateway"
)

// Store holds a completed response keyed by its Idempotency-Key. Implementations
// must be safe for concurrent use.
type Store interface {
	Get(key string) (*gateway.Response, bool)
	Put(key string, resp *gateway.Response)
}

// Config configures the plugin. Store overrides the in-memory default; TTL is
// the in-memory retention (default 10m); Header overrides the key header name
// (default Idempotency-Key).
type Config struct {
	Store  Store
	TTL    time.Duration
	Header string
}

// Plugin is the idempotency middleware returned by New.
type Plugin struct {
	store  Store
	header string
}

// New returns an idempotency plugin from cfg.
func New(cfgs ...Config) *Plugin {
	if len(cfgs) > 1 {
		panic("idempotency.New: at most one Config")
	}
	var cfg Config
	if len(cfgs) == 1 {
		cfg = cfgs[0]
	}
	header := cfg.Header
	if header == "" {
		header = "Idempotency-Key"
	}
	store := cfg.Store
	if store == nil {
		ttl := cfg.TTL
		if ttl <= 0 {
			ttl = 10 * time.Minute
		}
		store = newMemStore(ttl)
	}
	return &Plugin{store: store, header: header}
}

// Compile-time proof of the hooks this plugin binds — a signature
// drift here is a build error, not a silent non-binding at runtime.
var (
	_ gateway.Plugin      = (*Plugin)(nil)
	_ gateway.PluginDoc   = (*Plugin)(nil)
	_ gateway.Middlewarer = (*Plugin)(nil)
)

// PluginName surfaces in /rpc/_introspect.plugins[].
func (p *Plugin) PluginName() string { return "idempotency" }

// Doc surfaces a one-line description in /rpc/_introspect + the explorer.
func (p *Plugin) Doc() string {
	return "Replays the original 2xx response for a repeated Idempotency-Key so a retried mutation runs once."
}

// Wrap implements Middlewarer.
func (p *Plugin) Wrap(next gateway.Handler) gateway.Handler {
	return func(ctx context.Context, req *gateway.Request) *gateway.Response {
		key := req.Header.Get(p.header)
		// Only guard business /rpc calls that carry a key; framework paths and
		// unkeyed calls pass straight through.
		if key == "" || !strings.HasPrefix(req.Path, "/rpc/") || strings.HasPrefix(req.Path, "/rpc/_") {
			return next(ctx, req)
		}
		if cached, ok := p.store.Get(key); ok {
			return cached
		}
		resp := next(ctx, req)
		// Cache only a settled, replayable success. A non-2xx is re-tried (not
		// replayed); a stream can't be replayed.
		if resp != nil && resp.Stream == nil && resp.Status >= 200 && resp.Status < 300 {
			p.store.Put(key, resp)
		}
		return resp
	}
}

// ---- in-memory default store -------------------------------------------------

type memStore struct {
	mu        sync.Mutex
	m         map[string]memEntry
	ttl       time.Duration
	now       func() time.Time
	lastSweep time.Time
}

type memEntry struct {
	resp *gateway.Response
	at   time.Time
}

func newMemStore(ttl time.Duration) *memStore {
	return &memStore{m: map[string]memEntry{}, ttl: ttl, now: time.Now}
}

func (s *memStore) Get(key string) (*gateway.Response, bool) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep(now)
	e, ok := s.m[key]
	if !ok || now.Sub(e.at) > s.ttl {
		return nil, false
	}
	return e.resp, true
}

func (s *memStore) Put(key string, resp *gateway.Response) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep(now)
	s.m[key] = memEntry{resp: resp, at: now}
}

// sweep drops expired entries at most once per ttl, bounding the map.
func (s *memStore) sweep(now time.Time) {
	if now.Sub(s.lastSweep) < s.ttl {
		return
	}
	for k, e := range s.m {
		if now.Sub(e.at) > s.ttl {
			delete(s.m, k)
		}
	}
	s.lastSweep = now
}
