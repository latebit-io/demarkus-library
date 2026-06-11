package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrLoginRequired means the session cannot produce a usable bearer — the
// refresh token was rejected (revoked, expired, client deregistered) and the
// session has been torn down. The only recovery is a fresh login redirect.
var ErrLoginRequired = errors.New("session: login required")

// Tokens is one minted token set handed to Create or returned by an
// Authority refresh. Mirrors the oauth adapter's TokenSet without importing
// it — the inbound session package stays decoupled from the outbound broker
// adapter; the composition root shims between the two.
type Tokens struct {
	IDToken      string
	RefreshToken string
	// Expiry is the id_token expiry instant.
	Expiry time.Time
}

// Authority is the slice of the broker's OAuth surface the Manager needs.
//
//   - Refresh returns a Tokens whose RefreshToken may be empty, meaning the
//     authority does not rotate and the session keeps its current one.
//   - Refresh returning ErrGrantDead (wrapped or direct) marks the refresh
//     token unusable; any other error is transient (network, 5xx) and the
//     session survives it.
//   - Revoke is best-effort logout hygiene; idempotent.
type Authority interface {
	Refresh(ctx context.Context, refreshToken string) (Tokens, error)
	Revoke(ctx context.Context, refreshToken string) error
}

// ErrGrantDead is the error an Authority returns (or wraps) when the refresh
// token itself was rejected, as opposed to a transient failure. The
// composition-root shim maps the oauth adapter's ErrInvalidGrant onto it.
var ErrGrantDead = errors.New("session: refresh token rejected")

// DefaultRefreshSkew is how long before id_token expiry the Manager starts
// refreshing. Wide enough to cover clock drift between library and broker
// plus the /mcp call the token is about to make.
const DefaultRefreshSkew = 30 * time.Second

// Manager owns session lifecycle: mint on login, hand out live bearers,
// refresh near expiry exactly once per session at a time, tear down on
// logout or a dead grant. Safe for concurrent use.
type Manager struct {
	store     Store
	authority Authority
	skew      time.Duration
	now       func() time.Time

	// mu guards inflight. Each session gets one mutex so concurrent
	// requests on the SAME session single-flight the refresh, while
	// different sessions refresh independently.
	mu       sync.Mutex
	inflight map[string]*sync.Mutex
}

// NewManager wires a Store to an Authority. skew <= 0 takes
// DefaultRefreshSkew.
func NewManager(store Store, authority Authority, skew time.Duration) *Manager {
	if skew <= 0 {
		skew = DefaultRefreshSkew
	}
	return &Manager{
		store:     store,
		authority: authority,
		skew:      skew,
		now:       time.Now,
		inflight:  make(map[string]*sync.Mutex),
	}
}

// Create mints a session for a freshly exchanged token set and returns it.
// The caller (the auth callback handler) puts the ID in the cookie.
func (m *Manager) Create(ctx context.Context, t Tokens) (Session, error) {
	s := Session{
		ID:            NewID(),
		IDToken:       t.IDToken,
		RefreshToken:  t.RefreshToken,
		IDTokenExpiry: t.Expiry,
		CreatedAt:     m.now(),
	}
	if err := m.store.Save(ctx, s); err != nil {
		return Session{}, fmt.Errorf("session: save new session: %w", err)
	}
	return s, nil
}

// Token returns a bearer for the session that is good for at least the skew
// window, refreshing through the Authority if needed.
//
//   - ErrLoginRequired: no such session, or the grant is dead (session has
//     been deleted either way) — redirect to login.
//   - Transient refresh errors pass through unchanged; the session stays.
func (m *Manager) Token(ctx context.Context, sessionID string) (string, error) {
	s, err := m.store.Get(ctx, sessionID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", ErrLoginRequired
		}
		return "", fmt.Errorf("session: load: %w", err)
	}
	if m.fresh(s) {
		return s.IDToken, nil
	}

	// Stale: refresh under this session's single-flight lock.
	lock := m.sessionLock(sessionID)
	lock.Lock()
	defer lock.Unlock()

	// Re-read under the lock — a concurrent request may have refreshed
	// while this one waited.
	s, err = m.store.Get(ctx, sessionID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", ErrLoginRequired
		}
		return "", fmt.Errorf("session: reload: %w", err)
	}
	if m.fresh(s) {
		return s.IDToken, nil
	}

	t, err := m.authority.Refresh(ctx, s.RefreshToken)
	if err != nil {
		if errors.Is(err, ErrGrantDead) {
			// The refresh token mints nothing anymore; the session
			// is dead weight. Drop it so the next request goes
			// straight to login.
			_ = m.store.Delete(ctx, sessionID)
			return "", ErrLoginRequired
		}
		return "", fmt.Errorf("session: refresh: %w", err)
	}

	s.IDToken = t.IDToken
	s.IDTokenExpiry = t.Expiry
	if t.RefreshToken != "" {
		// Future-proofing: if the authority ever rotates, honor it.
		s.RefreshToken = t.RefreshToken
	}
	if err := m.store.Save(ctx, s); err != nil {
		return "", fmt.Errorf("session: save refreshed session: %w", err)
	}
	return s.IDToken, nil
}

// Logout revokes the refresh token (best effort — the broker's revoke is
// idempotent and a network failure must not block logout) and deletes the
// session.
func (m *Manager) Logout(ctx context.Context, sessionID string) error {
	s, err := m.store.Get(ctx, sessionID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil // already gone — logout is idempotent
		}
		return fmt.Errorf("session: load for logout: %w", err)
	}
	_ = m.authority.Revoke(ctx, s.RefreshToken)
	if err := m.store.Delete(ctx, sessionID); err != nil {
		return fmt.Errorf("session: delete: %w", err)
	}
	m.dropLock(sessionID)
	return nil
}

// fresh reports whether the id_token is still good for the skew window.
func (m *Manager) fresh(s Session) bool {
	return m.now().Add(m.skew).Before(s.IDTokenExpiry)
}

// sessionLock returns the single-flight mutex for a session, creating it on
// first use.
func (m *Manager) sessionLock(id string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.inflight[id]
	if !ok {
		l = &sync.Mutex{}
		m.inflight[id] = l
	}
	return l
}

// dropLock forgets a dead session's mutex so the inflight map does not grow
// with logins. Called on logout; dead-grant teardown leaves the entry until
// logout-or-restart, which is bounded by active-session count.
func (m *Manager) dropLock(id string) {
	m.mu.Lock()
	delete(m.inflight, id)
	m.mu.Unlock()
}
