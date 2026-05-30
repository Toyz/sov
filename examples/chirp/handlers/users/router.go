// Package users owns the user identity + follow graph end-to-end.
package users

import (
	"errors"
	"sync"

	"github.com/Toyz/sov/rpc"
)

// User is the public profile shape.
type User struct {
	ID      string `json:"id"`
	Handle  string `json:"handle"` // unique e.g. "alice"
	Display string `json:"display"`
}

// Store is the persistence boundary.
type Store interface {
	Insert(u *User) error
	GetByID(id string) (*User, error)
	GetByHandle(handle string) (*User, error)
	Follow(followerID, followeeID string) error
	Unfollow(followerID, followeeID string) error
	Following(followerID string) []string
	Followers(followeeID string) []string
}

// UserRouter exposes user methods.
type UserRouter struct {
	Store Store
}

// PublicMethods lets the gateway/authz default-allow these without
// authentication. Sign-up and profile-lookup are public; follow/unfollow
// and ListFollowing-for-self require auth.
func (r *UserRouter) PublicMethods() []string {
	return []string{"register", "get"}
}

// RegisterParams is the request body for Register. Public — Auth.register
// must be called first to mint the subject id this profile binds to.
type RegisterParams struct {
	Subject string `sov:"subject,0,required,title=Subject id,desc=From Auth.register,example=u_alice" json:"subject"`
	Handle  string `sov:"handle,1,required,title=Handle,example=alice" json:"handle"`
	Display string `sov:"display,2,title=Display name,example=Alice" json:"display"`
}

// Register creates a new user profile bound to a previously-minted
// subject id. Public method — sign-up is a two-call dance: Auth.register
// → User.register. The two domains share the subject id; nothing else.
func (r *UserRouter) Register(ctx *rpc.Context, p *RegisterParams) (*User, error) {
	if p.Subject == "" {
		return nil, rpc.BadRequest("subject required (call Auth/register first)")
	}
	if p.Handle == "" {
		return nil, rpc.BadRequest("handle required")
	}
	if existing, _ := r.Store.GetByHandle(p.Handle); existing != nil {
		return nil, rpc.Conflict("handle already taken")
	}
	u := &User{ID: p.Subject, Handle: p.Handle, Display: p.Display}
	if u.Display == "" {
		u.Display = p.Handle
	}
	if err := r.Store.Insert(u); err != nil {
		return nil, rpc.Internal("insert: %v", err)
	}
	return u, nil
}

// GetMe returns the caller's user record. Reads the gateway-injected
// subject from ctx and looks up the profile row.
func (r *UserRouter) GetMe(ctx *rpc.Context) (*User, error) {
	uid, err := rpc.RequireSubject(ctx)
	if err != nil {
		return nil, err
	}
	u, lookupErr := r.Store.GetByID(uid)
	if lookupErr != nil || u == nil {
		return nil, rpc.NotFound("user not found")
	}
	return u, nil
}

// GetParams is the request body for Get.
type GetParams struct {
	ID     string `json:"id,omitempty"`
	Handle string `json:"handle,omitempty"`
}

// Get returns a user by id or handle.
func (r *UserRouter) Get(ctx *rpc.Context, p *GetParams) (*User, error) {
	if p.ID == "" && p.Handle == "" {
		return nil, rpc.BadRequest("id or handle required")
	}
	var (
		u   *User
		err error
	)
	if p.ID != "" {
		u, err = r.Store.GetByID(p.ID)
	} else {
		u, err = r.Store.GetByHandle(p.Handle)
	}
	if err != nil || u == nil {
		return nil, rpc.NotFound("user not found")
	}
	return u, nil
}

// FollowParams is the request body for Follow / Unfollow.
type FollowParams struct {
	FolloweeID string `json:"followee_id"`
}

// Follow makes the caller follow followee.
func (r *UserRouter) Follow(ctx *rpc.Context, p *FollowParams) (map[string]bool, error) {
	uid, err := rpc.RequireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if p.FolloweeID == "" || p.FolloweeID == uid {
		return nil, rpc.BadRequest("invalid followee_id")
	}
	if err := r.Store.Follow(uid, p.FolloweeID); err != nil {
		return nil, rpc.Internal("follow: %v", err)
	}
	return map[string]bool{"ok": true}, nil
}

// Unfollow reverses Follow.
func (r *UserRouter) Unfollow(ctx *rpc.Context, p *FollowParams) (map[string]bool, error) {
	uid, err := rpc.RequireSubject(ctx)
	if err != nil {
		return nil, err
	}
	if err := r.Store.Unfollow(uid, p.FolloweeID); err != nil {
		return nil, rpc.Internal("unfollow: %v", err)
	}
	return map[string]bool{"ok": true}, nil
}

// ListFollowingParams is the request body for ListFollowing.
type ListFollowingParams struct {
	UserID string `json:"user_id,omitempty"` // defaults to caller
}

// ListFollowingResult is the response body.
type ListFollowingResult struct {
	FolloweeIDs []string `json:"followee_ids"`
}

// ListFollowing returns the user ids the given user follows. If UserID
// is empty, defaults to the caller (which then requires auth).
func (r *UserRouter) ListFollowing(ctx *rpc.Context, p *ListFollowingParams) (*ListFollowingResult, error) {
	uid := p.UserID
	if uid == "" {
		caller, err := rpc.RequireSubject(ctx)
		if err != nil {
			return nil, rpc.BadRequest("user_id required (or call with auth)")
		}
		uid = caller
	}
	return &ListFollowingResult{FolloweeIDs: r.Store.Following(uid)}, nil
}

// ---- MemoryStore -----------------------------------------------------------

// MemoryStore is the in-memory impl.
type MemoryStore struct {
	mu        sync.RWMutex
	byID      map[string]*User
	byHandle  map[string]*User
	following map[string]map[string]struct{} // follower → set of followees
	followers map[string]map[string]struct{} // followee → set of followers
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		byID:      map[string]*User{},
		byHandle:  map[string]*User{},
		following: map[string]map[string]struct{}{},
		followers: map[string]map[string]struct{}{},
	}
}

// Insert implements Store.
func (m *MemoryStore) Insert(u *User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, dup := m.byID[u.ID]; dup {
		return errors.New("duplicate id")
	}
	if _, dup := m.byHandle[u.Handle]; dup {
		return errors.New("duplicate handle")
	}
	m.byID[u.ID] = u
	m.byHandle[u.Handle] = u
	return nil
}

// GetByID implements Store.
func (m *MemoryStore) GetByID(id string) (*User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	u, ok := m.byID[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return u, nil
}

// GetByHandle implements Store.
func (m *MemoryStore) GetByHandle(h string) (*User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	u, ok := m.byHandle[h]
	if !ok {
		return nil, errors.New("not found")
	}
	return u, nil
}

// Follow implements Store.
func (m *MemoryStore) Follow(follower, followee string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.following[follower] == nil {
		m.following[follower] = map[string]struct{}{}
	}
	if m.followers[followee] == nil {
		m.followers[followee] = map[string]struct{}{}
	}
	m.following[follower][followee] = struct{}{}
	m.followers[followee][follower] = struct{}{}
	return nil
}

// Unfollow implements Store.
func (m *MemoryStore) Unfollow(follower, followee string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.following[follower], followee)
	delete(m.followers[followee], follower)
	return nil
}

// Following implements Store.
func (m *MemoryStore) Following(follower string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.following[follower]))
	for id := range m.following[follower] {
		out = append(out, id)
	}
	return out
}

// Followers implements Store.
func (m *MemoryStore) Followers(followee string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.followers[followee]))
	for id := range m.followers[followee] {
		out = append(out, id)
	}
	return out
}
