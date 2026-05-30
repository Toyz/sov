package gateway

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Resolver locates a service by wire name and returns either a local
// dispatch (handlers in-process) or a remote endpoint (HTTP-proxy). A
// gateway holds a chain of resolvers and tries them in order — the first
// hit wins. Implementations must be safe for concurrent use.
type Resolver interface {
	Resolve(ctx context.Context, service string) (*Endpoint, bool)
	// Services returns a snapshot of every service this resolver knows
	// about, used by the gateway's /_health aggregator.
	Services() []string
	// Introspectables returns the subset of Services() that opt in to
	// /_introspect probing. Returning a nil/empty slice means "no
	// introspection allowed" — the gateway will skip the HTTP probe and
	// the service stays out of the aggregated type catalog.
	Introspectables() []string
}

// Endpoint is what a resolver returns. Three shapes:
//   - Local=true: dispatch against the in-process engine
//   - Peer non-nil: dispatch against another *Gateway in the same
//     binary (nested PEMM, no HTTP hop)
//   - RemoteAddr set: HTTP-proxy to that base URL
//
// Peer takes precedence over Local; RemoteAddr is the fallback.
type Endpoint struct {
	Local      bool
	RemoteAddr string  // e.g. "http://widgets-pod-7:8080" — no trailing slash
	Peer       Handler // when set, dispatch via this in-process Handler (nested PEMM)
}

// LocalResolver wraps an in-process Engine. Hit when the requested
// router is registered locally. Every locally-registered router is
// always introspectable — the Describe path is in-process and cheap.
type LocalResolver struct {
	hasRouter func(name string) bool
	routers   func() []string
}

// NewLocalResolverFunc lets the gateway pass plain lookup closures so we
// don't have to import rpc inside this file (resolver.go stays
// dependency-free; the engine-aware glue lives in gateway.go).
func NewLocalResolverFunc(hasRouter func(name string) bool, routers func() []string) *LocalResolver {
	return &LocalResolver{hasRouter: hasRouter, routers: routers}
}

// Resolve implements Resolver.
func (l *LocalResolver) Resolve(_ context.Context, service string) (*Endpoint, bool) {
	if l.hasRouter == nil || !l.hasRouter(service) {
		return nil, false
	}
	return &Endpoint{Local: true}, true
}

// Services implements Resolver.
func (l *LocalResolver) Services() []string {
	if l.routers == nil {
		return nil
	}
	return l.routers()
}

// Introspectables implements Resolver. Local routers are always opt-in.
func (l *LocalResolver) Introspectables() []string { return l.Services() }

// EntryOptions configure a RegisterResolver entry.
type EntryOptions struct {
	// Introspectable, when true, lets the gateway probe this remote
	// pod's /rpc/_introspect on aggregation. When false (default), the
	// remote stays out of the catalog — useful for services that
	// haven't enabled introspection or want to opt out of the
	// org-wide type browser.
	Introspectable bool
}

// RegisterEntry is one service registration: where the service lives, when
// the registration expires, and whether it opts into introspect
// aggregation. The unit a RegisterStore persists.
type RegisterEntry struct {
	Address        string
	ExpiresAt      time.Time
	Introspectable bool
}

// RegisterStore is the pluggable backing store for the registry's
// service→address map. Default is in-memory, per replica. Supply a shared
// impl (e.g. Redis) via WithRegisterStore so a FLEET of gateway replicas
// shares one mesh view — otherwise each replica only knows the pods whose
// heartbeat happened to land on it (partial-view routing/health drift).
//
// Implementations must be safe for concurrent use. Put/Delete are writes
// at heartbeat rate (from /rpc/_register). Snapshot is read by the
// resolver to refill its local read cache — NOT per request — so a remote
// store is hit at most once per refresh tick, never on the dispatch hot
// path. ReapExpired drops entries past ExpiresAt and reports whether
// anything changed; a store with native TTL (Redis) may no-op + return
// false.
type RegisterStore interface {
	Put(service string, e RegisterEntry)
	Delete(service string)
	Snapshot() map[string]RegisterEntry
	ReapExpired(now time.Time) (changed bool)
}

// RegisterResolver holds a TTL-backed map of service → remote address,
// populated by services that POST /rpc/_register on startup and refresh
// via heartbeat. Reads are served from a lock-free local cache; the
// backing store (RegisterStore) is pluggable for multi-replica meshes.
type RegisterResolver struct {
	store    RegisterStore
	cache    atomic.Pointer[map[string]RegisterEntry] // local read snapshot
	now      func() time.Time
	refresh  time.Duration
	stopOnce sync.Once
	stop     chan struct{}
	onChange func() // optional cache-invalidation hook (set by Gateway)
}

// RegisterResolverOption configures a RegisterResolver.
type RegisterResolverOption func(*RegisterResolver)

// WithRegisterStore overrides the backing store (default in-memory). Pass
// a shared (e.g. Redis) store so gateway replicas share one mesh view.
func WithRegisterStore(s RegisterStore) RegisterResolverOption {
	return func(r *RegisterResolver) {
		if s != nil {
			r.store = s
		}
	}
}

// WithRegisterRefresh makes the resolver pull store.Snapshot() every d to
// pick up registrations made on OTHER replicas against a shared store.
// Only one replica receives a given pod's heartbeat, so the rest must poll
// to converge. 0 (default) = local-only (single replica / in-memory).
func WithRegisterRefresh(d time.Duration) RegisterResolverOption {
	return func(r *RegisterResolver) { r.refresh = d }
}

// NewRegisterResolver returns a resolver with a background reaper that runs
// at evictInterval (defaults to 5s). Pass options to swap the store or
// enable cross-replica refresh.
func NewRegisterResolver(evictInterval time.Duration, opts ...RegisterResolverOption) *RegisterResolver {
	if evictInterval <= 0 {
		evictInterval = 5 * time.Second
	}
	r := &RegisterResolver{
		store: newMemRegisterStore(),
		now:   time.Now,
		stop:  make(chan struct{}),
	}
	for _, o := range opts {
		o(r)
	}
	r.reload()
	go r.reap(evictInterval)
	if r.refresh > 0 {
		go r.refreshLoop(r.refresh)
	}
	return r
}

// snapshot returns the current local read cache (never nil).
func (r *RegisterResolver) snapshot() map[string]RegisterEntry {
	if p := r.cache.Load(); p != nil {
		return *p
	}
	return map[string]RegisterEntry{}
}

// reload refills the local read cache from the store.
func (r *RegisterResolver) reload() {
	s := r.store.Snapshot()
	r.cache.Store(&s)
}

// Put inserts or refreshes a service entry. Defaults to
// Introspectable=true for the common case (every pod opts in unless it
// declared otherwise on _register). Use PutEntry to set flags
// explicitly.
func (r *RegisterResolver) Put(service, address string, ttl time.Duration) {
	r.PutEntry(service, address, ttl, EntryOptions{Introspectable: true})
}

// PutEntry inserts or refreshes a service entry with explicit options.
func (r *RegisterResolver) PutEntry(service, address string, ttl time.Duration, opts EntryOptions) {
	r.store.Put(service, RegisterEntry{
		Address:        address,
		ExpiresAt:      r.now().Add(ttl),
		Introspectable: opts.Introspectable,
	})
	r.reload()
	if r.onChange != nil {
		r.onChange()
	}
}

// Delete removes a service entry. No-op if absent.
func (r *RegisterResolver) Delete(service string) {
	_, present := r.snapshot()[service]
	r.store.Delete(service)
	if present {
		r.reload()
		if r.onChange != nil {
			r.onChange()
		}
	}
}

// Resolve implements Resolver. Reads the lock-free local cache.
func (r *RegisterResolver) Resolve(_ context.Context, service string) (*Endpoint, bool) {
	e, ok := r.snapshot()[service]
	if !ok || r.now().After(e.ExpiresAt) {
		return nil, false
	}
	return &Endpoint{RemoteAddr: e.Address}, true
}

// Services implements Resolver.
func (r *RegisterResolver) Services() []string {
	snap := r.snapshot()
	out := make([]string, 0, len(snap))
	now := r.now()
	for name, e := range snap {
		if now.After(e.ExpiresAt) {
			continue
		}
		out = append(out, name)
	}
	return out
}

// AddressGroup returns unique-address → service-names-served-at-that-
// address. Live entries only (TTL respected). Used by the gateway's
// introspect + health cascades to probe each federated team gateway
// exactly once rather than once per service it fronts.
func (r *RegisterResolver) AddressGroup() map[string][]string {
	snap := r.snapshot()
	out := map[string][]string{}
	now := r.now()
	for name, e := range snap {
		if now.After(e.ExpiresAt) {
			continue
		}
		out[e.Address] = append(out[e.Address], name)
	}
	return out
}

// Introspectables implements Resolver — only entries that registered
// with Introspectable=true are returned.
func (r *RegisterResolver) Introspectables() []string {
	snap := r.snapshot()
	out := make([]string, 0, len(snap))
	now := r.now()
	for name, e := range snap {
		if now.After(e.ExpiresAt) || !e.Introspectable {
			continue
		}
		out = append(out, name)
	}
	return out
}

// Close stops the background reaper. Safe to call multiple times.
func (r *RegisterResolver) Close() {
	r.stopOnce.Do(func() { close(r.stop) })
}

func (r *RegisterResolver) reap(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-t.C:
			if r.store.ReapExpired(r.now()) {
				r.reload()
				if r.onChange != nil {
					r.onChange()
				}
			}
		}
	}
}

// refreshLoop pulls the shared store into the local cache every interval so
// registrations made on other replicas converge here. Fires onChange only
// when the snapshot actually changed, so the catalog isn't invalidated on
// every idle tick.
func (r *RegisterResolver) refreshLoop(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-t.C:
			old := r.snapshot()
			r.reload()
			if snapshotChanged(old, r.snapshot()) && r.onChange != nil {
				r.onChange()
			}
		}
	}
}

// snapshotChanged reports whether two registration snapshots differ in
// membership or address (cheap — ignores ExpiresAt churn from heartbeats).
func snapshotChanged(a, b map[string]RegisterEntry) bool {
	if len(a) != len(b) {
		return true
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || av.Address != bv.Address || av.Introspectable != bv.Introspectable {
			return true
		}
	}
	return false
}

// memRegisterStore is the default in-memory RegisterStore (single replica).
type memRegisterStore struct {
	mu sync.RWMutex
	m  map[string]RegisterEntry
}

func newMemRegisterStore() *memRegisterStore {
	return &memRegisterStore{m: map[string]RegisterEntry{}}
}

func (s *memRegisterStore) Put(service string, e RegisterEntry) {
	s.mu.Lock()
	s.m[service] = e
	s.mu.Unlock()
}

func (s *memRegisterStore) Delete(service string) {
	s.mu.Lock()
	delete(s.m, service)
	s.mu.Unlock()
}

func (s *memRegisterStore) Snapshot() map[string]RegisterEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]RegisterEntry, len(s.m))
	for k, v := range s.m {
		out[k] = v
	}
	return out
}

func (s *memRegisterStore) ReapExpired(now time.Time) bool {
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

// dynamicChain wraps a base Chain plus a plugin-added slice. The
// gateway swaps its g.resolver to a dynamicChain at construction so
// `gw.Use(myResolver)` extends the chain without rebuilding state.
// Reads take a snapshot under RLock; writes happen via append/replace.
type dynamicChain struct {
	mu      sync.RWMutex
	base    []Resolver
	plugins []Resolver
}

func newDynamicChain(base ...Resolver) *dynamicChain {
	return &dynamicChain{base: append([]Resolver{}, base...)}
}

func (d *dynamicChain) addPlugin(r Resolver) {
	d.mu.Lock()
	d.plugins = append(d.plugins, r)
	d.mu.Unlock()
}

func (d *dynamicChain) links() []Resolver {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]Resolver, 0, len(d.base)+len(d.plugins))
	out = append(out, d.base...)
	out = append(out, d.plugins...)
	return out
}

// Resolve implements Resolver.
func (d *dynamicChain) Resolve(ctx context.Context, service string) (*Endpoint, bool) {
	for _, r := range d.links() {
		if ep, ok := r.Resolve(ctx, service); ok {
			return ep, true
		}
	}
	return nil, false
}

// Services implements Resolver — union, first-link priority.
func (d *dynamicChain) Services() []string {
	seen := map[string]struct{}{}
	var out []string
	for _, r := range d.links() {
		for _, name := range r.Services() {
			if _, dup := seen[name]; dup {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	return out
}

// Introspectables implements Resolver — union, first-link priority.
func (d *dynamicChain) Introspectables() []string {
	seen := map[string]struct{}{}
	var out []string
	for _, r := range d.links() {
		for _, name := range r.Introspectables() {
			if _, dup := seen[name]; dup {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	return out
}
