// Package chirps owns the chirp entity end-to-end. One service, one
// table, one set of verbs — PEMM's "one entity per service" rule.
package chirps

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/Toyz/sov/rpc"
)

// Chirp is the canonical entity.
type Chirp struct {
	ID       string    `json:"id"`
	AuthorID string    `json:"author_id"`
	Body     string    `json:"body"`
	PostedAt time.Time `json:"posted_at"`
}

// Store is the persistence boundary. The MVP ships an in-memory impl;
// real deployments substitute a Postgres-backed one with the same
// interface and zero handler changes.
type Store interface {
	Insert(c *Chirp) error
	Get(id string) (*Chirp, error)
	ListByAuthors(ids []string, limit int) []*Chirp
}

// Store extension methods used by the mod-only Delete demo. Kept on
// the interface so substituted stores stay drop-in.
type DeletableStore interface {
	Store
	Delete(id string) error
	List(limit int) []*Chirp
}

// ChirpRouter exposes the chirp methods. Name must end in "Router" — the
// engine reflects that suffix to derive the wire namespace ("Chirp").
type ChirpRouter struct {
	Store DeletableStore
}

// PublicMethods lets the gateway/authz default-allow these without
// authentication. The chirp public timeline / detail endpoints stay
// readable to anonymous visitors.
func (r *ChirpRouter) PublicMethods() []string {
	return []string{"list", "get", "listByAuthors"}
}

// PostParams is the request body for Post.
type PostParams struct {
	Body string `sov:"body,0,required,title=Chirp text,desc=Up to 280 characters,example=Hello sov mesh!" json:"body"`
}

// Post creates one chirp authored by the caller.
func (r *ChirpRouter) Post(ctx *rpc.Context, p *PostParams) (*Chirp, error) {
	uid, err := rpc.RequireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if p.Body == "" {
		return nil, rpc.BadRequest("body required")
	}
	if len(p.Body) > 280 {
		return nil, rpc.BadRequest("body too long (max 280)")
	}
	c := &Chirp{
		ID:       newID(),
		AuthorID: uid,
		Body:     p.Body,
		PostedAt: time.Now().UTC(),
	}
	if err := r.Store.Insert(c); err != nil {
		return nil, rpc.Internal("insert: %v", err)
	}
	return c, nil
}

// DeleteParams is the request body for Delete.
type DeleteParams struct {
	ID string `json:"id"`
}

// Delete removes one chirp. Mod-only: the authz service is configured
// to reject non-mod callers with 403 BEFORE the request reaches this
// method, so the handler does NOT need to re-check the role itself.
// It still calls RequireSubject as defense-in-depth in case authz is
// misconfigured (no policy server bound).
func (r *ChirpRouter) Delete(ctx *rpc.Context, p *DeleteParams) (map[string]bool, error) {
	if _, err := rpc.RequireSubject(ctx); err != nil {
		return nil, err
	}
	if p.ID == "" {
		return nil, rpc.BadRequest("id required")
	}
	if err := r.Store.Delete(p.ID); err != nil {
		return nil, rpc.NotFound("chirp %q not found", p.ID)
	}
	return map[string]bool{"ok": true}, nil
}

// ListParams is the request body for List.
type ListParams struct {
	Limit int `json:"limit,omitempty"`
}

// List returns the latest chirps across all authors. Public — anonymous
// callers see the same view authenticated callers do.
func (r *ChirpRouter) List(ctx *rpc.Context, p *ListParams) (*ListByAuthorsResult, error) {
	limit := p.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return &ListByAuthorsResult{Chirps: r.Store.List(limit)}, nil
}

// GetParams is the request body for Get.
type GetParams struct {
	ID string `json:"id"`
}

// Get returns one chirp by id, or 404.
func (r *ChirpRouter) Get(ctx *rpc.Context, p *GetParams) (*Chirp, error) {
	if p.ID == "" {
		return nil, rpc.BadRequest("id required")
	}
	c, err := r.Store.Get(p.ID)
	if err != nil {
		return nil, rpc.NotFound("chirp %q not found", p.ID)
	}
	return c, nil
}

// ListByAuthorsParams is the request body for ListByAuthors.
type ListByAuthorsParams struct {
	AuthorIDs []string `json:"author_ids"`
	Limit     int      `json:"limit,omitempty"`
}

// ListByAuthorsResult is the response body.
type ListByAuthorsResult struct {
	Chirps []*Chirp `json:"chirps"`
}

// ListByAuthors returns the latest chirps by any of the given author
// ids, newest first. Used by FeedService to assemble a timeline.
func (r *ChirpRouter) ListByAuthors(ctx *rpc.Context, p *ListByAuthorsParams) (*ListByAuthorsResult, error) {
	if len(p.AuthorIDs) == 0 {
		return &ListByAuthorsResult{Chirps: []*Chirp{}}, nil
	}
	limit := p.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	chirps := r.Store.ListByAuthors(p.AuthorIDs, limit)
	return &ListByAuthorsResult{Chirps: chirps}, nil
}

// ---- MemoryStore -----------------------------------------------------------

// MemoryStore is the in-memory Store impl shipped for examples and tests.
type MemoryStore struct {
	mu     sync.RWMutex
	chirps map[string]*Chirp
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{chirps: map[string]*Chirp{}}
}

// Insert implements Store.
func (m *MemoryStore) Insert(c *Chirp) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, dup := m.chirps[c.ID]; dup {
		return errors.New("duplicate id")
	}
	m.chirps[c.ID] = c
	return nil
}

// Get implements Store.
func (m *MemoryStore) Get(id string) (*Chirp, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.chirps[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return c, nil
}

// Delete implements DeletableStore.
func (m *MemoryStore) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.chirps[id]; !ok {
		return errors.New("not found")
	}
	delete(m.chirps, id)
	return nil
}

// List implements DeletableStore.
func (m *MemoryStore) List(limit int) []*Chirp {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Chirp, 0, len(m.chirps))
	for _, c := range m.chirps {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PostedAt.After(out[j].PostedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// ListByAuthors implements Store.
func (m *MemoryStore) ListByAuthors(ids []string, limit int) []*Chirp {
	m.mu.RLock()
	defer m.mu.RUnlock()
	want := map[string]struct{}{}
	for _, id := range ids {
		want[id] = struct{}{}
	}
	var out []*Chirp
	for _, c := range m.chirps {
		if _, ok := want[c.AuthorID]; ok {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PostedAt.After(out[j].PostedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// newID is a tiny non-crypto id generator for the demo. Real deployments
// use uuid or sqlite rowid. Kept here so the example has zero non-stdlib
// dependencies in its handlers.
func newID() string {
	now := time.Now().UnixNano()
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	out := make([]byte, 0, 8)
	for now > 0 {
		out = append(out, alphabet[now%int64(len(alphabet))])
		now /= int64(len(alphabet))
	}
	return string(out)
}
