// Package session holds the library-card state for logged-in readers. The
// browser carries only an opaque CSPRNG session id in an HttpOnly cookie;
// the id_token and refresh token live server-side in a Store — they never
// cross the XSS boundary (ADR 0004).
//
// The package depends on no broker specifics: the Manager refreshes tokens
// through the small Authority interface, satisfied by a shim over the oauth
// adapter at the composition root. Cookie reading/writing is the web
// middleware's job (Phase 1b step 2.6); this package is storage + lifecycle.
package session

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sync"
	"time"
)

// ErrNotFound means no live session exists for the presented id — never
// minted, expired, or logged out. The middleware answers with a login
// redirect.
var ErrNotFound = errors.New("session: not found")

// Session is one reader's library card: the broker tokens plus bookkeeping.
// Values are copied in and out of the Store so callers never share memory
// with it.
type Session struct {
	// ID is the opaque cookie value — 32 CSPRNG bytes, base64url.
	ID string
	// IDToken is the bearer presented to the broker's /mcp gateway.
	IDToken string
	// RefreshToken mints replacement id_tokens; the broker does not
	// rotate it, so it lives as long as the session.
	RefreshToken string
	// IDTokenExpiry is when IDToken stops being accepted.
	IDTokenExpiry time.Time
	// CreatedAt anchors the session's absolute lifetime.
	CreatedAt time.Time
}

// Store persists sessions keyed by id. Implementations are safe for
// concurrent use. The interface exists so the in-memory store can be swapped
// for Redis/SQL without touching the Manager.
type Store interface {
	// Get returns the session for id, or ErrNotFound.
	Get(ctx context.Context, id string) (Session, error)
	// Save inserts or replaces the session under its ID.
	Save(ctx context.Context, s Session) error
	// Delete removes the session; deleting an absent id is a no-op.
	Delete(ctx context.Context, id string) error
}

// MemoryStore is the Phase 1b Store: a mutex-guarded map with an absolute
// per-session TTL. Sessions vanish on process restart — acceptable for a
// single-instance deployment; readers just log in again.
type MemoryStore struct {
	ttl time.Duration
	now func() time.Time

	mu       sync.RWMutex
	sessions map[string]Session
}

var _ Store = (*MemoryStore)(nil)

// NewMemoryStore creates a store whose sessions expire ttl after CreatedAt
// (the refresh-token lifetime is the natural ceiling — the broker's is 90
// days). Expired sessions are dropped lazily on Get and in bulk by Sweep.
func NewMemoryStore(ttl time.Duration) *MemoryStore {
	return &MemoryStore{ttl: ttl, now: time.Now, sessions: make(map[string]Session)}
}

// Get returns the live session for id, dropping it if past TTL.
func (m *MemoryStore) Get(_ context.Context, id string) (Session, error) {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok {
		return Session{}, ErrNotFound
	}
	if m.expired(s) {
		m.mu.Lock()
		// Re-check under the write lock: a Save may have replaced the
		// entry since the read.
		if cur, ok := m.sessions[id]; ok && m.expired(cur) {
			delete(m.sessions, id)
		}
		m.mu.Unlock()
		return Session{}, ErrNotFound
	}
	return s, nil
}

// Save inserts or replaces s under s.ID.
func (m *MemoryStore) Save(_ context.Context, s Session) error {
	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()
	return nil
}

// Delete removes the session for id.
func (m *MemoryStore) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
	return nil
}

// Sweep drops all expired sessions and reports how many went. The
// composition root runs this on a ticker; Get's lazy expiry keeps
// correctness even if it never runs.
func (m *MemoryStore) Sweep() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id, s := range m.sessions {
		if m.expired(s) {
			delete(m.sessions, id)
			n++
		}
	}
	return n
}

func (m *MemoryStore) expired(s Session) bool {
	return m.now().Sub(s.CreatedAt) >= m.ttl
}

// NewID returns a fresh opaque session id: 32 CSPRNG bytes, base64url, no
// padding. crypto/rand.Read is documented (Go ≥1.24) to never return an
// error; the check still panics explicitly so a hypothetical entropy failure
// can never silently yield a predictable session id.
func NewID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("session: entropy source unavailable: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
