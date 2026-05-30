package signing

import (
	"context"
	"crypto/ed25519"
	"errors"
	"sync"
)

// PublicKeyStore looks up the Ed25519 public key registered for a
// session id. Implementations must be safe for concurrent use.
//
// Consumers implement this on top of their durable store (Redis,
// Postgres, NATS KV) — this package ships an in-memory implementation
// useful for tests and single-process examples.
type PublicKeyStore interface {
	Get(ctx context.Context, sessionID string) (ed25519.PublicKey, error)
}

// ErrSessionNotFound is what a PublicKeyStore returns when sessionID is
// unknown. The Validator turns this into SESSION_EXPIRED.
var ErrSessionNotFound = errors.New("signing: session not found")

// MemoryStore is a goroutine-safe in-memory PublicKeyStore.
type MemoryStore struct {
	mu   sync.RWMutex
	keys map[string]ed25519.PublicKey
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{keys: map[string]ed25519.PublicKey{}}
}

// Put inserts or replaces a session's public key.
func (m *MemoryStore) Put(sessionID string, key ed25519.PublicKey) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keys[sessionID] = key
}

// Delete removes a session.
func (m *MemoryStore) Delete(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.keys, sessionID)
}

// Get implements PublicKeyStore.
func (m *MemoryStore) Get(_ context.Context, sessionID string) (ed25519.PublicKey, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	k, ok := m.keys[sessionID]
	if !ok {
		return nil, ErrSessionNotFound
	}
	return k, nil
}
