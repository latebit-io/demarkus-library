package session

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock is a mutex-guarded movable now() for manager tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeAuthority counts refreshes and serves a configurable response.
type fakeAuthority struct {
	mu           sync.Mutex
	refreshCalls atomic.Int64
	revokeCalls  atomic.Int64
	lastRevoked  string
	refreshErr   error
	revokeErr    error
	tokens       Tokens
	delay        time.Duration // simulates broker latency inside Refresh
}

func (f *fakeAuthority) Refresh(_ context.Context, _ string) (Tokens, error) {
	f.refreshCalls.Add(1)
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refreshErr != nil {
		return Tokens{}, f.refreshErr
	}
	return f.tokens, nil
}

func (f *fakeAuthority) Revoke(_ context.Context, token string) error {
	f.revokeCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastRevoked = token
	return f.revokeErr
}

func testSealer(t *testing.T, fill byte) *Sealer {
	t.Helper()
	s, err := NewSealer(bytes.Repeat([]byte{fill}, KeySize))
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	return s
}

func newTestManager(t *testing.T, clock *fakeClock, auth *fakeAuthority) *Manager {
	t.Helper()
	m := NewManager(testSealer(t, 7), auth, Config{TTL: 24 * time.Hour, Skew: 30 * time.Second})
	m.now = clock.now
	return m
}

// create mints a session cookie or fails the test.
func create(t *testing.T, m *Manager, tok Tokens) string {
	t.Helper()
	live, err := m.Create(tok)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return live.Cookie
}

func TestNewSealerRejectsKeyLength(t *testing.T) {
	for _, n := range []int{0, 16, 31, 33} {
		if _, err := NewSealer(make([]byte, n)); err == nil {
			t.Errorf("NewSealer(%d bytes) accepted", n)
		}
	}
}

func TestSealerOpenRejects(t *testing.T) {
	sealer := testSealer(t, 1)
	value, err := sealer.seal(purposeSession, Session{ID: "id"})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(value)
	raw[len(raw)-1] ^= 0xff
	tampered := base64.RawURLEncoding.EncodeToString(raw)

	tests := []struct {
		name    string
		sealer  *Sealer
		purpose string
		value   string
	}{
		{name: "tampered", sealer: sealer, purpose: purposeSession, value: tampered},
		{name: "other key", sealer: testSealer(t, 2), purpose: purposeSession, value: value},
		{name: "other purpose", sealer: sealer, purpose: purposeLogin, value: value},
		{name: "not base64", sealer: sealer, purpose: purposeSession, value: "@@@"},
		{name: "shorter than a nonce", sealer: sealer, purpose: purposeSession, value: "AAAA"},
		{name: "empty", sealer: sealer, purpose: purposeSession, value: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s Session
			if err := tt.sealer.open(tt.purpose, tt.value, &s); !errors.Is(err, ErrInvalid) {
				t.Errorf("open = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestSealerRefusesOversizedValue(t *testing.T) {
	_, err := testSealer(t, 1).seal(purposeSession, Session{IDToken: strings.Repeat("x", maxSealedLen)})
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("seal = %v, want ErrTooLarge", err)
	}
}

// TestSessionCookieFitsProductionToken seals a broker-shaped id_token (ES256,
// twenty groups) and keeps the headroom visible in the test log.
func TestSessionCookieFitsProductionToken(t *testing.T) {
	groups := make([]string, 20)
	for i := range groups {
		groups[i] = fmt.Sprintf(`"platform-engineering-team-%02d@latebit.io"`, i)
	}
	claims := `{"iss":"https://broker.knowledge.demarkus.io","sub":"112233445566778899001",` +
		`"aud":["library-web"],"exp":1793000000,"iat":1792996400,"email":"reader@latebit.io",` +
		`"email_verified":true,"groups":[` + strings.Join(groups, ",") + `],"hd":"latebit.io"}`
	enc := base64.RawURLEncoding
	idToken := enc.EncodeToString([]byte(`{"alg":"ES256","kid":"0123456789abcdef","typ":"JWT"}`)) + "." +
		enc.EncodeToString([]byte(claims)) + "." + enc.EncodeToString(make([]byte, 64))

	m := newTestManager(t, newFakeClock(), &fakeAuthority{})
	value := create(t, m, Tokens{IDToken: idToken, RefreshToken: strings.Repeat("r", 100), Expiry: time.Now()})
	t.Logf("id_token %d bytes, sealed cookie %d bytes (limit %d)", len(idToken), len(value), maxSealedLen)
	if len(value) > maxSealedLen {
		t.Errorf("sealed cookie %d bytes over limit %d", len(value), maxSealedLen)
	}
}

func TestResolveFreshPassesThrough(t *testing.T) {
	clock := newFakeClock()
	auth := &fakeAuthority{}
	m := newTestManager(t, clock, auth)
	value := create(t, m, Tokens{IDToken: "live", RefreshToken: "ref", Expiry: clock.now().Add(time.Hour)})

	live, err := m.Resolve(t.Context(), value)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if live.Session.IDToken != "live" || live.Session.ID == "" {
		t.Errorf("session = %+v", live.Session)
	}
	if live.Cookie != "" {
		t.Error("a fresh session must not re-seal the cookie")
	}
	if want := clock.now().Add(24 * time.Hour); !live.Expires.Equal(want) {
		t.Errorf("Expires = %v, want login + TTL %v", live.Expires, want)
	}
	if auth.refreshCalls.Load() != 0 {
		t.Errorf("refreshed a fresh token %d times", auth.refreshCalls.Load())
	}
}

func TestResolveRefreshesWithinSkew(t *testing.T) {
	clock := newFakeClock()
	auth := &fakeAuthority{tokens: Tokens{IDToken: "minted", Expiry: clock.now().Add(time.Hour)}}
	m := newTestManager(t, clock, auth)
	// Expires in 10s — inside the 30s skew window, so stale on arrival.
	value := create(t, m, Tokens{IDToken: "stale", RefreshToken: "ref-1", Expiry: clock.now().Add(10 * time.Second)})

	live, err := m.Resolve(t.Context(), value)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if live.Session.IDToken != "minted" || live.Cookie == "" {
		t.Fatalf("refresh did not re-seal: %+v", live)
	}
	// No rotation: an empty RefreshToken in the response keeps ref-1.
	if live.Session.RefreshToken != "ref-1" {
		t.Errorf("RefreshToken = %q, want ref-1 kept", live.Session.RefreshToken)
	}

	// The re-sealed cookie carries the new token: no second refresh, same id.
	again, err := m.Resolve(t.Context(), live.Cookie)
	if err != nil {
		t.Fatalf("Resolve(re-sealed): %v", err)
	}
	if again.Session.IDToken != "minted" || again.Session.ID != live.Session.ID {
		t.Errorf("re-sealed session = %+v, want minted under id %q", again.Session, live.Session.ID)
	}
	if n := auth.refreshCalls.Load(); n != 1 {
		t.Errorf("refresh calls = %d, want 1", n)
	}
}

func TestResolveSingleFlight(t *testing.T) {
	clock := newFakeClock()
	auth := &fakeAuthority{
		tokens: Tokens{IDToken: "minted", Expiry: clock.now().Add(time.Hour)},
		delay:  20 * time.Millisecond, // hold the refresh open so racers pile up
	}
	m := newTestManager(t, clock, auth)
	value := create(t, m, Tokens{IDToken: "stale", RefreshToken: "ref", Expiry: clock.now().Add(time.Second)})

	const racers = 16
	var wg sync.WaitGroup
	errs := make([]error, racers)
	toks := make([]string, racers)
	for i := range racers {
		wg.Go(func() {
			live, err := m.Resolve(t.Context(), value)
			toks[i], errs[i] = live.Session.IDToken, err
		})
	}
	wg.Wait()

	for i := range racers {
		if errs[i] != nil {
			t.Fatalf("racer %d: %v", i, errs[i])
		}
		if toks[i] != "minted" {
			t.Errorf("racer %d token = %q, want minted", i, toks[i])
		}
	}
	if n := auth.refreshCalls.Load(); n != 1 {
		t.Errorf("refresh calls = %d, want exactly 1 (single flight)", n)
	}
}

func TestResolveDeadGrantRequiresLogin(t *testing.T) {
	clock := newFakeClock()
	auth := &fakeAuthority{refreshErr: fmt.Errorf("broker said: %w", ErrGrantDead)}
	m := newTestManager(t, clock, auth)
	value := create(t, m, Tokens{IDToken: "stale", RefreshToken: "dead", Expiry: clock.now().Add(time.Second)})

	if _, err := m.Resolve(t.Context(), value); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("Resolve = %v, want ErrLoginRequired", err)
	}
}

func TestResolveTransientErrorKeepsCookie(t *testing.T) {
	clock := newFakeClock()
	auth := &fakeAuthority{refreshErr: errors.New("connection refused")}
	m := newTestManager(t, clock, auth)
	value := create(t, m, Tokens{IDToken: "stale", RefreshToken: "ref", Expiry: clock.now().Add(time.Second)})

	_, err := m.Resolve(t.Context(), value)
	if err == nil || errors.Is(err, ErrLoginRequired) {
		t.Fatalf("Resolve = %v, want transient error", err)
	}

	// The broker recovers; the same cookie refreshes.
	auth.mu.Lock()
	auth.refreshErr = nil
	auth.tokens = Tokens{IDToken: "minted", Expiry: clock.now().Add(time.Hour)}
	auth.mu.Unlock()
	if live, err := m.Resolve(t.Context(), value); err != nil || live.Session.IDToken != "minted" {
		t.Errorf("Resolve after recovery = %+v, %v", live, err)
	}
}

func TestResolvePastLifetimeRequiresLogin(t *testing.T) {
	clock := newFakeClock()
	m := newTestManager(t, clock, &fakeAuthority{})
	value := create(t, m, Tokens{IDToken: "live", RefreshToken: "ref", Expiry: clock.now().Add(48 * time.Hour)})

	clock.advance(24 * time.Hour)
	if _, err := m.Resolve(t.Context(), value); !errors.Is(err, ErrLoginRequired) {
		t.Errorf("Resolve past TTL = %v, want ErrLoginRequired", err)
	}
}

func TestResolveUnreadableCookieRequiresLogin(t *testing.T) {
	m := newTestManager(t, newFakeClock(), &fakeAuthority{})
	if _, err := m.Resolve(t.Context(), "not-a-sealed-value"); !errors.Is(err, ErrLoginRequired) {
		t.Errorf("Resolve = %v, want ErrLoginRequired", err)
	}
}

// TestReplicasShareSessions is the HA property: any replica holding the key
// resolves a cookie another replica sealed; a replica on another key cannot.
func TestReplicasShareSessions(t *testing.T) {
	clock := newFakeClock()
	minter := newTestManager(t, clock, &fakeAuthority{})
	value := create(t, minter, Tokens{IDToken: "live", RefreshToken: "ref", Expiry: clock.now().Add(time.Hour)})

	sibling := newTestManager(t, clock, &fakeAuthority{})
	if live, err := sibling.Resolve(t.Context(), value); err != nil || live.Session.IDToken != "live" {
		t.Errorf("sibling Resolve = %+v, %v", live, err)
	}

	stranger := NewManager(testSealer(t, 9), &fakeAuthority{}, Config{TTL: time.Hour})
	stranger.now = clock.now
	if _, err := stranger.Resolve(t.Context(), value); !errors.Is(err, ErrLoginRequired) {
		t.Errorf("other-key Resolve = %v, want ErrLoginRequired", err)
	}
}

func TestLogout(t *testing.T) {
	clock := newFakeClock()
	auth := &fakeAuthority{}
	m := newTestManager(t, clock, auth)
	value := create(t, m, Tokens{IDToken: "tok", RefreshToken: "ref-x", Expiry: clock.now().Add(time.Hour)})

	if err := m.Logout(t.Context(), value); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	auth.mu.Lock()
	revoked := auth.lastRevoked
	auth.mu.Unlock()
	if revoked != "ref-x" {
		t.Errorf("revoked %q, want ref-x", revoked)
	}

	// An unreadable cookie has nothing to revoke.
	if err := m.Logout(t.Context(), "garbage"); err != nil {
		t.Errorf("Logout(garbage) = %v, want nil", err)
	}
	if n := auth.revokeCalls.Load(); n != 1 {
		t.Errorf("revoke calls = %d, want 1", n)
	}

	// A failed revoke surfaces so the caller can log it.
	auth.mu.Lock()
	auth.revokeErr = errors.New("broker down")
	auth.mu.Unlock()
	if err := m.Logout(t.Context(), value); err == nil {
		t.Error("Logout swallowed a revoke failure")
	}
}

func TestPendingLogins(t *testing.T) {
	clock := newFakeClock()
	sealer := testSealer(t, 3)
	p := NewPendingLogins(sealer)
	p.now = clock.now

	value, err := p.Seal(PendingLogin{State: "state-1", Verifier: "verifier-1", ReturnTo: "/d/index.md"})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	pl, err := p.Open(value, "state-1")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if pl.Verifier != "verifier-1" || pl.ReturnTo != "/d/index.md" {
		t.Errorf("pending = %+v", pl)
	}

	sessionValue, err := sealer.seal(purposeSession, Session{ID: "id"})
	if err != nil {
		t.Fatalf("seal session: %v", err)
	}
	expired, err := p.Seal(PendingLogin{State: "state-2"})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	clock.advance(PendingTTL)

	tests := []struct {
		name  string
		value string
		state string
	}{
		{name: "other state", value: value, state: "state-x"},
		{name: "empty state", value: value, state: ""},
		{name: "expired", value: expired, state: "state-2"},
		{name: "session cookie", value: sessionValue, state: "state-1"},
		{name: "garbage", value: "garbage", state: "state-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := p.Open(tt.value, tt.state); err == nil {
				t.Error("Open accepted")
			}
		})
	}
}

func TestNewIDUnique(t *testing.T) {
	a, b := NewID(), NewID()
	if a == b {
		t.Error("two session ids identical — randomness broken")
	}
	if len(a) != 43 { // 32 bytes base64url unpadded
		t.Errorf("id length = %d, want 43", len(a))
	}
}
