package gateway

import (
	"context"
	"sort"
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
// aggregation. The unit a RegisterStore persists. Multiple entries with the
// same service name but different Address are REPLICAS of that service — the
// resolver load-balances across them (W1.1).
type RegisterEntry struct {
	Address        string
	ExpiresAt      time.Time
	Introspectable bool
}

// RegisterStore is the pluggable backing store for the registry's
// service→replicas map. Default is in-memory, per replica. Supply a shared
// impl (e.g. Redis) via WithRegisterStore so a FLEET of gateway replicas
// shares one mesh view — otherwise each replica only knows the pods whose
// heartbeat happened to land on it (partial-view routing/health drift).
//
// A service may have MORE THAN ONE live entry: each distinct Address is a
// replica. Put upserts by (service, entry.Address) — a second address adds a
// replica; the same address refreshes in place. Delete removes ONE replica by
// (service, address).
//
// Implementations must be safe for concurrent use. Put/Delete are writes at
// heartbeat rate (from /rpc/_register). Snapshot is read by the resolver to
// refill its local read cache — NOT per request — so a remote store is hit at
// most once per refresh tick, never on the dispatch hot path. ReapExpired
// drops entries past ExpiresAt and reports whether anything changed; a store
// with native TTL (Redis) may no-op + return false.
type RegisterStore interface {
	Put(service string, e RegisterEntry)
	Delete(service, address string)
	Snapshot() map[string][]RegisterEntry
	ReapExpired(now time.Time) (changed bool)
}

// EndpointPicker chooses ONE replica from a service's live set on the dispatch
// hot path. The default is round-robin; supply your own (least-connections,
// zone-aware, sticky) via WithEndpointPicker. live is sorted by Address, so a
// stateless picker can index it deterministically. Must be safe for concurrent
// use and cheap — it runs per request.
type EndpointPicker interface {
	Pick(service string, live []RegisterEntry) (RegisterEntry, bool)
}

// roundRobinPicker is the default EndpointPicker: a per-service counter that
// rotates across the (Address-sorted) live set so load spreads evenly.
type roundRobinPicker struct {
	mu       sync.Mutex
	counters map[string]uint64
}

func newRoundRobinPicker() *roundRobinPicker {
	return &roundRobinPicker{counters: map[string]uint64{}}
}

func (p *roundRobinPicker) Pick(service string, live []RegisterEntry) (RegisterEntry, bool) {
	n := len(live)
	if n == 0 {
		return RegisterEntry{}, false
	}
	p.mu.Lock()
	i := p.counters[service]
	p.counters[service] = i + 1
	p.mu.Unlock()
	return live[int(i%uint64(n))], true
}

// RegisterResolver holds a TTL-backed map of service → remote replicas,
// populated by services that POST /rpc/_register on startup and refresh
// via heartbeat. Reads are served from a lock-free local cache; the
// backing store (RegisterStore) is pluggable for multi-replica meshes.
type RegisterResolver struct {
	store    RegisterStore
	cache    atomic.Pointer[map[string][]RegisterEntry] // local read snapshot
	now      func() time.Time
	refresh  time.Duration
	picker   EndpointPicker
	stopOnce sync.Once
	stop     chan struct{}
	onChange func() // optional cache-invalidation hook (set by Gateway)
	// breakerOpen, when set by the Gateway, reports whether a given upstream
	// address has an OPEN circuit breaker. Resolve skips open replicas so a
	// tripped pod stops receiving traffic — unless ALL replicas are open, in
	// which case it falls back to the full set so half-open recovery can probe.
	breakerOpen func(addr string) bool
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

// WithEndpointPicker overrides the replica-selection strategy (default
// round-robin). See EndpointPicker.
func WithEndpointPicker(p EndpointPicker) RegisterResolverOption {
	return func(r *RegisterResolver) {
		if p != nil {
			r.picker = p
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
		store:  newMemRegisterStore(),
		now:    time.Now,
		picker: newRoundRobinPicker(),
		stop:   make(chan struct{}),
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
func (r *RegisterResolver) snapshot() map[string][]RegisterEntry {
	if p := r.cache.Load(); p != nil {
		return *p
	}
	return map[string][]RegisterEntry{}
}

// reload refills the local read cache from the store.
func (r *RegisterResolver) reload() {
	s := r.store.Snapshot()
	r.cache.Store(&s)
}

// Put inserts or refreshes a service replica. Defaults to
// Introspectable=true for the common case (every pod opts in unless it
// declared otherwise on _register). Use PutEntry to set flags
// explicitly.
func (r *RegisterResolver) Put(service, address string, ttl time.Duration) {
	r.PutEntry(service, address, ttl, EntryOptions{Introspectable: true})
}

// PutEntry inserts or refreshes a service replica with explicit options. A new
// address for an existing service is added as a replica; the same address
// refreshes in place.
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

// Delete removes ONE replica (service, address). No-op if absent.
func (r *RegisterResolver) Delete(service, address string) {
	_, present := r.liveEntry(service, address)
	r.store.Delete(service, address)
	if present {
		r.reload()
		if r.onChange != nil {
			r.onChange()
		}
	}
}

// liveEntry reports whether a specific replica is currently present (regardless
// of TTL — used only to decide whether a Delete actually changed anything).
func (r *RegisterResolver) liveEntry(service, address string) (RegisterEntry, bool) {
	for _, e := range r.snapshot()[service] {
		if e.Address == address {
			return e, true
		}
	}
	return RegisterEntry{}, false
}

// liveReplicas returns the non-expired replicas for service, sorted by Address
// so a stateless picker rotates deterministically.
func (r *RegisterResolver) liveReplicas(service string) []RegisterEntry {
	entries := r.snapshot()[service]
	if len(entries) == 0 {
		return nil
	}
	now := r.now()
	live := make([]RegisterEntry, 0, len(entries))
	for _, e := range entries {
		if now.After(e.ExpiresAt) {
			continue
		}
		live = append(live, e)
	}
	sort.Slice(live, func(i, j int) bool { return live[i].Address < live[j].Address })
	return live
}

// Resolve implements Resolver. Reads the lock-free local cache, filters out
// replicas whose breaker is open (unless all are open), and picks one via the
// EndpointPicker.
func (r *RegisterResolver) Resolve(_ context.Context, service string) (*Endpoint, bool) {
	live := r.liveReplicas(service)
	if len(live) == 0 {
		return nil, false
	}
	// Skip replicas with an open breaker, but never strand the service: if
	// every replica is open, fall through with the full set so the picker
	// still hands one out and half-open recovery can probe it.
	if r.breakerOpen != nil {
		healthy := make([]RegisterEntry, 0, len(live))
		for _, e := range live {
			if !r.breakerOpen(e.Address) {
				healthy = append(healthy, e)
			}
		}
		if len(healthy) > 0 {
			live = healthy
		}
	}
	picked, ok := r.picker.Pick(service, live)
	if !ok {
		return nil, false
	}
	return &Endpoint{RemoteAddr: picked.Address}, true
}

// Services implements Resolver — every service with at least one live replica.
func (r *RegisterResolver) Services() []string {
	snap := r.snapshot()
	out := make([]string, 0, len(snap))
	now := r.now()
	for name, entries := range snap {
		if anyLive(entries, now) {
			out = append(out, name)
		}
	}
	return out
}

// AddressGroup returns unique-address → service-names-served-at-that-
// address. Live replicas only (TTL respected). Used by the gateway's
// introspect + health cascades to probe each federated team gateway
// exactly once rather than once per service it fronts.
func (r *RegisterResolver) AddressGroup() map[string][]string {
	snap := r.snapshot()
	out := map[string][]string{}
	now := r.now()
	for name, entries := range snap {
		for _, e := range entries {
			if now.After(e.ExpiresAt) {
				continue
			}
			out[e.Address] = append(out[e.Address], name)
		}
	}
	return out
}

// Introspectables implements Resolver — a service is introspectable if any of
// its live replicas registered with Introspectable=true.
func (r *RegisterResolver) Introspectables() []string {
	snap := r.snapshot()
	out := make([]string, 0, len(snap))
	now := r.now()
	for name, entries := range snap {
		for _, e := range entries {
			if now.After(e.ExpiresAt) || !e.Introspectable {
				continue
			}
			out = append(out, name)
			break
		}
	}
	return out
}

func anyLive(entries []RegisterEntry, now time.Time) bool {
	for _, e := range entries {
		if !now.After(e.ExpiresAt) {
			return true
		}
	}
	return false
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
// membership or the set of addresses/introspect flags per service (cheap —
// ignores ExpiresAt churn from heartbeats).
func snapshotChanged(a, b map[string][]RegisterEntry) bool {
	if len(a) != len(b) {
		return true
	}
	for name, av := range a {
		bv, ok := b[name]
		if !ok || len(av) != len(bv) {
			return true
		}
		am := addrFlagSet(av)
		bm := addrFlagSet(bv)
		if len(am) != len(bm) {
			return true
		}
		for addr, intro := range am {
			if bIntro, ok := bm[addr]; !ok || bIntro != intro {
				return true
			}
		}
	}
	return false
}

// addrFlagSet maps each replica's address to its Introspectable flag.
func addrFlagSet(entries []RegisterEntry) map[string]bool {
	m := make(map[string]bool, len(entries))
	for _, e := range entries {
		m[e.Address] = e.Introspectable
	}
	return m
}

// memRegisterStore is the default in-memory RegisterStore (single replica).
// It keys service → address → entry so a second address for the same service
// is a replica, not an overwrite.
type memRegisterStore struct {
	mu sync.RWMutex
	m  map[string]map[string]RegisterEntry
}

func newMemRegisterStore() *memRegisterStore {
	return &memRegisterStore{m: map[string]map[string]RegisterEntry{}}
}

func (s *memRegisterStore) Put(service string, e RegisterEntry) {
	s.mu.Lock()
	if s.m[service] == nil {
		s.m[service] = map[string]RegisterEntry{}
	}
	s.m[service][e.Address] = e
	s.mu.Unlock()
}

func (s *memRegisterStore) Delete(service, address string) {
	s.mu.Lock()
	if reps := s.m[service]; reps != nil {
		delete(reps, address)
		if len(reps) == 0 {
			delete(s.m, service)
		}
	}
	s.mu.Unlock()
}

func (s *memRegisterStore) Snapshot() map[string][]RegisterEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string][]RegisterEntry, len(s.m))
	for svc, reps := range s.m {
		list := make([]RegisterEntry, 0, len(reps))
		for _, e := range reps {
			list = append(list, e)
		}
		out[svc] = list
	}
	return out
}

func (s *memRegisterStore) ReapExpired(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for svc, reps := range s.m {
		for addr, e := range reps {
			if now.After(e.ExpiresAt) {
				delete(reps, addr)
				changed = true
			}
		}
		if len(reps) == 0 {
			delete(s.m, svc)
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
