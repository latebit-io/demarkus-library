package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sync/singleflight"
)

// ErrLoginRequired means the cookie cannot produce a usable bearer: it does
// not open, the session is past its lifetime, or the refresh token was
// rejected. The only recovery is a fresh login redirect.
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
//   - Revoke is logout hygiene; idempotent.
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

// Config sets a Manager's lifetimes.
type Config struct {
	// TTL is the absolute session lifetime, counted from login.
	TTL time.Duration
	// Skew is how early before id_token expiry a refresh starts; <= 0
	// takes DefaultRefreshSkew.
	Skew time.Duration
}

// Live is a session resolved for one request. Cookie is the value to set,
// empty when the presented cookie stands; Expires is its absolute end.
type Live struct {
	Session Session
	Cookie  string
	Expires time.Time
}

// Manager owns session lifecycle: seal on login, resolve a live bearer per
// request, refresh near expiry, revoke on logout. It holds no per-session
// state, so replicas sharing the sealing key are interchangeable.
type Manager struct {
	sealer    *Sealer
	authority Authority
	ttl       time.Duration
	skew      time.Duration
	now       func() time.Time

	// refresh single-flights one session's concurrent refreshes on this
	// replica; an entry lives only while its refresh runs.
	refresh singleflight.Group
}

// NewManager wires a Sealer to an Authority.
func NewManager(sealer *Sealer, authority Authority, cfg Config) *Manager {
	skew := cfg.Skew
	if skew <= 0 {
		skew = DefaultRefreshSkew
	}
	return &Manager{sealer: sealer, authority: authority, ttl: cfg.TTL, skew: skew, now: time.Now}
}

// Create mints a session for a freshly exchanged token set, sealed.
func (m *Manager) Create(t Tokens) (Live, error) {
	s := Session{
		ID:            NewID(),
		IDToken:       t.IDToken,
		RefreshToken:  t.RefreshToken,
		IDTokenExpiry: t.Expiry,
		CreatedAt:     m.now(),
	}
	value, err := m.sealer.seal(purposeSession, s)
	if err != nil {
		return Live{}, fmt.Errorf("session: seal new session: %w", err)
	}
	return m.live(s, value), nil
}

// Resolve opens a cookie value into a session whose bearer is good for at
// least the skew window, refreshing through the Authority when it is not.
//
//   - ErrLoginRequired: redirect to login.
//   - Transient refresh errors pass through; the cookie stays usable.
func (m *Manager) Resolve(ctx context.Context, value string) (Live, error) {
	s, err := m.open(value)
	if err != nil {
		return Live{}, err
	}
	if m.fresh(s) {
		return m.live(s, ""), nil
	}
	v, err, _ := m.refresh.Do(s.ID, func() (any, error) {
		// Detached: waiters share this refresh, so it must not die with the
		// request that started it. The authority's HTTP timeout bounds it.
		return m.refreshed(context.WithoutCancel(ctx), s)
	})
	if err != nil {
		return Live{}, err
	}
	live, ok := v.(Live)
	if !ok {
		return Live{}, fmt.Errorf("session: refresh flight returned %T", v)
	}
	return live, nil
}

// Logout revokes the session's refresh token. A sealed cookie cannot be
// recalled, so its id_token stays usable until expiry; the revoked refresh
// token stops it there.
func (m *Manager) Logout(ctx context.Context, value string) error {
	var s Session
	if err := m.sealer.open(purposeSession, value, &s); err != nil {
		// Nothing readable to revoke; the caller clears the cookie.
		return nil
	}
	if err := m.authority.Revoke(ctx, s.RefreshToken); err != nil {
		return fmt.Errorf("session: revoke: %w", err)
	}
	return nil
}

// open unseals value and enforces the absolute lifetime.
func (m *Manager) open(value string) (Session, error) {
	var s Session
	if err := m.sealer.open(purposeSession, value, &s); err != nil {
		return Session{}, fmt.Errorf("%w: %w", ErrLoginRequired, err)
	}
	if !m.now().Before(m.live(s, "").Expires) {
		return Session{}, fmt.Errorf("%w: session past its lifetime", ErrLoginRequired)
	}
	return s, nil
}

// refreshed mints a new id_token for s and re-seals it.
func (m *Manager) refreshed(ctx context.Context, s Session) (Live, error) {
	t, err := m.authority.Refresh(ctx, s.RefreshToken)
	if err != nil {
		if errors.Is(err, ErrGrantDead) {
			return Live{}, fmt.Errorf("%w: %w", ErrLoginRequired, err)
		}
		return Live{}, fmt.Errorf("session: refresh: %w", err)
	}
	s.IDToken = t.IDToken
	s.IDTokenExpiry = t.Expiry
	if t.RefreshToken != "" {
		// Future-proofing: if the authority ever rotates, honor it.
		s.RefreshToken = t.RefreshToken
	}
	value, err := m.sealer.seal(purposeSession, s)
	if err != nil {
		return Live{}, fmt.Errorf("session: seal refreshed session: %w", err)
	}
	return m.live(s, value), nil
}

// live pairs s with the cookie value to set and its absolute expiry.
func (m *Manager) live(s Session, cookie string) Live {
	return Live{Session: s, Cookie: cookie, Expires: s.CreatedAt.Add(m.ttl)}
}

// fresh reports whether the id_token is still good for the skew window.
func (m *Manager) fresh(s Session) bool {
	return m.now().Add(m.skew).Before(s.IDTokenExpiry)
}
