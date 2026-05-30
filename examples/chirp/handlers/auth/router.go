// Package auth is the chirp demo's identity surface — register a
// credential, exchange a handle+password for a session token, verify
// tokens for the gateway. AuthRouter holds NO reference to the
// UserService; subject ids are the only shared concept between the
// two domains.
package auth

import (
	"sync"
	"time"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/rpc"
)

// AuthRouter exposes the auth surface. Credentials and sessions live
// in-memory for the demo. The router has zero references to the User
// service — identity (subject) and profile (user record) are split
// domains. A frontend calls Auth/register to mint a subject and then
// User/register with that subject to create the profile.
type AuthRouter struct {
	Credentials *CredentialStore
	Sessions    *SessionStore
}

// PublicMethods lets the gateway/authz skip the auth gate for these
// methods — they ARE the auth surface, so requiring auth would be a
// chicken-and-egg failure.
func (r *AuthRouter) PublicMethods() []string {
	return []string{"register", "login", "verify"}
}

// RegisterParams is the request body for Register.
type RegisterParams struct {
	Handle   string `sov:"handle,0,required,title=Handle,desc=Unique alphanumeric handle,example=alice" json:"handle"`
	Password string `sov:"password,1,required,title=Password,desc=Demo only — never hashed,example=hunter2" json:"password"`
}

// RegisterResult is the response body for Register.
type RegisterResult struct {
	Subject string `json:"subject"`
}

// Register mints a credential row and returns the freshly-minted
// subject id. The frontend then calls User/register with this subject
// to create the profile row.
func (r *AuthRouter) Register(ctx *rpc.Context, p *RegisterParams) (*RegisterResult, error) {
	if p.Handle == "" || p.Password == "" {
		return nil, rpc.BadRequest("handle and password required")
	}
	sub, err := r.Credentials.Insert(p.Handle, p.Password)
	if err != nil {
		return nil, rpc.Conflict("%v", err)
	}
	return &RegisterResult{Subject: sub}, nil
}

// LoginParams is the request body for Login.
type LoginParams struct {
	Handle   string `sov:"handle,0,required,title=Handle,example=alice" json:"handle"`
	Password string `sov:"password,1,required,title=Password,example=hunter2" json:"password"`
}

// LoginResult is the response body for Login. Subject + token only.
// No user record — that's the User service's job.
type LoginResult struct {
	Token     string    `json:"token"`
	Subject   string    `json:"subject"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Login verifies the credential and issues a session token.
func (r *AuthRouter) Login(ctx *rpc.Context, p *LoginParams) (*LoginResult, error) {
	if p.Handle == "" || p.Password == "" {
		return nil, rpc.BadRequest("handle and password required")
	}
	sub, ok := r.Credentials.Verify(p.Handle, p.Password)
	if !ok {
		return nil, rpc.Unauthorized("bad credentials")
	}
	tok, exp := r.Sessions.Issue(sub)
	return &LoginResult{Token: tok, Subject: sub, ExpiresAt: exp}, nil
}

// Verify is the gateway-facing endpoint. Resolves a bearer token to
// identity-only Claims. NO user lookup — the gateway caches the result
// until ExpiresAt.
func (r *AuthRouter) Verify(ctx *rpc.Context, p *gateway.VerifyParams) (*gateway.Claims, error) {
	if p.Token == "" {
		return nil, rpc.Unauthorized("missing token")
	}
	sub, exp := r.Sessions.Lookup(p.Token)
	if sub == "" {
		return nil, rpc.Unauthorized("invalid or expired token")
	}
	return &gateway.Claims{
		Subject:   sub,
		Issuer:    "chirp-auth",
		ExpiresAt: exp,
	}, nil
}

// LogoutParams is the request body for Logout.
type LogoutParams struct {
	Token string `json:"token"`
}

// Logout invalidates a session token. Authz gates this — callers must
// be authenticated to log themselves out (callers prove they own the
// token by holding it).
func (r *AuthRouter) Logout(ctx *rpc.Context, p *LogoutParams) (map[string]bool, error) {
	if _, err := rpc.RequireSubject(ctx); err != nil {
		return nil, err
	}
	r.Sessions.Revoke(p.Token)
	return map[string]bool{"ok": true}, nil
}

// ---- CredentialStore ------------------------------------------------------

// CredentialStore is an in-memory handle+password → subject_id map.
// Demo grade. Production wires a hashed-password DB.
type CredentialStore struct {
	mu         sync.RWMutex
	bySubject  map[string]credential
	byHandle   map[string]string // handle → subject
	subjectSeq int
}

type credential struct {
	Handle   string
	Password string // demo only: cleartext
}

// NewCredentialStore returns an empty store.
func NewCredentialStore() *CredentialStore {
	return &CredentialStore{
		bySubject: map[string]credential{},
		byHandle:  map[string]string{},
	}
}

// Insert creates a credential row, returns the minted subject id.
func (s *CredentialStore) Insert(handle, password string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.byHandle[handle]; dup {
		return "", &handleTaken{Handle: handle}
	}
	s.subjectSeq++
	sub := "u_" + handle
	s.bySubject[sub] = credential{Handle: handle, Password: password}
	s.byHandle[handle] = sub
	return sub, nil
}

// Verify checks a handle+password and returns the subject id if it matches.
func (s *CredentialStore) Verify(handle, password string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sub, ok := s.byHandle[handle]
	if !ok {
		return "", false
	}
	c := s.bySubject[sub]
	if c.Password != password {
		return "", false
	}
	return sub, true
}

type handleTaken struct{ Handle string }

func (e *handleTaken) Error() string { return "handle " + e.Handle + " already taken" }

// ---- SessionStore ---------------------------------------------------------

// SessionStore is a tiny in-memory token store. Issue stamps an opaque
// token; Lookup returns the subject id; Revoke drops it.
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]sessionEntry
}

type sessionEntry struct {
	Subject   string
	ExpiresAt time.Time
}

// NewSessionStore returns an empty store.
func NewSessionStore() *SessionStore {
	return &SessionStore{sessions: map[string]sessionEntry{}}
}

// Issue creates a token bound to subjectID. TTL is one hour for the demo.
func (s *SessionStore) Issue(subjectID string) (string, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp := time.Now().Add(1 * time.Hour).UTC()
	tok := "tok_" + subjectID + "_" + randSuffix()
	s.sessions[tok] = sessionEntry{Subject: subjectID, ExpiresAt: exp}
	return tok, exp
}

// Lookup returns the subject id and expiry for a token, or ("",zero) if
// unknown/revoked.
func (s *SessionStore) Lookup(token string) (string, time.Time) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.sessions[token]
	if !ok {
		return "", time.Time{}
	}
	return e.Subject, e.ExpiresAt
}

// Revoke drops a token.
func (s *SessionStore) Revoke(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, token)
}

// randSuffix is intentionally weak — demo only. Production picks
// crypto/rand. Kept here so the example has zero non-stdlib deps.
func randSuffix() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	out := make([]byte, 8)
	now := nowNS()
	for i := range out {
		out[i] = alphabet[now%int64(len(alphabet))]
		now /= int64(len(alphabet))
	}
	return string(out)
}

// nowNS pulled into a var so tests can override deterministically if needed.
var nowNS = func() int64 {
	return time.Now().UnixNano()
}
