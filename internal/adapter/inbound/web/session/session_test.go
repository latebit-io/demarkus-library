package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock is a mutex-guarded movable now() for store/manager tests.
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
	f.lastRevoked = token
	f.mu.Unlock()
	return nil
}

func TestMemoryStoreCRUDAndTTL(t *testing.T) {
	clock := newFakeClock()
	store := NewMemoryStore(time.Hour)
	store.now = clock.now
	ctx := context.Background()

	s := Session{ID: "id-1", IDToken: "tok", RefreshToken: "ref", CreatedAt: clock.now()}
	if err := store.Save(ctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Get(ctx, "id-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.IDToken != "tok" {
		t.Errorf("IDToken = %q", got.IDToken)
	}

	if _, err := store.Get(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(missing) = %v, want ErrNotFound", err)
	}

	clock.advance(time.Hour + time.Second)
	if _, err := store.Get(ctx, "id-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(expired) = %v, want ErrNotFound", err)
	}

	// Sweep drops expired entries in bulk.
	s2 := Session{ID: "id-2", CreatedAt: clock.now()}
	store.Save(ctx, s2)
	clock.advance(2 * time.Hour)
	if n := store.Sweep(); n != 1 {
		t.Errorf("Sweep = %d, want 1", n)
	}

	if err := store.Delete(ctx, "never-existed"); err != nil {
		t.Errorf("Delete(absent) = %v, want nil", err)
	}
}

func newTestManager(clock *fakeClock, auth *fakeAuthority) (*Manager, *MemoryStore) {
	store := NewMemoryStore(24 * time.Hour)
	store.now = clock.now
	m := NewManager(store, auth, 30*time.Second)
	m.now = clock.now
	return m, store
}

func TestTokenFreshPassesThrough(t *testing.T) {
	clock := newFakeClock()
	auth := &fakeAuthority{}
	m, _ := newTestManager(clock, auth)
	ctx := context.Background()

	s, err := m.Create(ctx, Tokens{IDToken: "live", RefreshToken: "ref", Expiry: clock.now().Add(time.Hour)})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	tok, err := m.Token(ctx, s.ID)
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "live" {
		t.Errorf("token = %q, want live", tok)
	}
	if auth.refreshCalls.Load() != 0 {
		t.Errorf("refreshed a fresh token %d times", auth.refreshCalls.Load())
	}
}

func TestTokenRefreshesWithinSkew(t *testing.T) {
	clock := newFakeClock()
	auth := &fakeAuthority{tokens: Tokens{IDToken: "minted", Expiry: clock.now().Add(time.Hour)}}
	m, store := newTestManager(clock, auth)
	ctx := context.Background()

	// Expires in 10s — inside the 30s skew window, so stale on arrival.
	s, _ := m.Create(ctx, Tokens{IDToken: "stale", RefreshToken: "ref-1", Expiry: clock.now().Add(10 * time.Second)})

	tok, err := m.Token(ctx, s.ID)
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "minted" {
		t.Errorf("token = %q, want minted", tok)
	}
	if auth.refreshCalls.Load() != 1 {
		t.Errorf("refresh calls = %d, want 1", auth.refreshCalls.Load())
	}
	// No rotation: empty RefreshToken in the response keeps ref-1.
	got, _ := store.Get(ctx, s.ID)
	if got.RefreshToken != "ref-1" {
		t.Errorf("RefreshToken = %q, want ref-1 kept", got.RefreshToken)
	}
}

func TestTokenSingleFlight(t *testing.T) {
	clock := newFakeClock()
	auth := &fakeAuthority{
		tokens: Tokens{IDToken: "minted", Expiry: clock.now().Add(time.Hour)},
		delay:  20 * time.Millisecond, // hold the refresh open so racers pile up
	}
	m, _ := newTestManager(clock, auth)
	ctx := context.Background()

	s, _ := m.Create(ctx, Tokens{IDToken: "stale", RefreshToken: "ref", Expiry: clock.now().Add(time.Second)})

	const racers = 16
	var wg sync.WaitGroup
	errs := make([]error, racers)
	toks := make([]string, racers)
	for i := range racers {
		wg.Go(func() {
			toks[i], errs[i] = m.Token(ctx, s.ID)
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

func TestTokenDeadGrantTearsDownSession(t *testing.T) {
	clock := newFakeClock()
	auth := &fakeAuthority{refreshErr: fmt.Errorf("broker said: %w", ErrGrantDead)}
	m, store := newTestManager(clock, auth)
	ctx := context.Background()

	s, _ := m.Create(ctx, Tokens{IDToken: "stale", RefreshToken: "dead", Expiry: clock.now().Add(time.Second)})

	if _, err := m.Token(ctx, s.ID); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("Token = %v, want ErrLoginRequired", err)
	}
	if _, err := store.Get(ctx, s.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("session survived a dead grant: %v", err)
	}
}

func TestTokenTransientErrorKeepsSession(t *testing.T) {
	clock := newFakeClock()
	auth := &fakeAuthority{refreshErr: errors.New("connection refused")}
	m, store := newTestManager(clock, auth)
	ctx := context.Background()

	s, _ := m.Create(ctx, Tokens{IDToken: "stale", RefreshToken: "ref", Expiry: clock.now().Add(time.Second)})

	_, err := m.Token(ctx, s.ID)
	if err == nil || errors.Is(err, ErrLoginRequired) {
		t.Fatalf("Token = %v, want transient error", err)
	}
	if _, err := store.Get(ctx, s.ID); err != nil {
		t.Errorf("session should survive a transient refresh failure: %v", err)
	}
}

func TestTokenUnknownSession(t *testing.T) {
	clock := newFakeClock()
	m, _ := newTestManager(clock, &fakeAuthority{})
	if _, err := m.Token(context.Background(), "no-such"); !errors.Is(err, ErrLoginRequired) {
		t.Errorf("Token = %v, want ErrLoginRequired", err)
	}
}

func TestLogout(t *testing.T) {
	clock := newFakeClock()
	auth := &fakeAuthority{}
	m, store := newTestManager(clock, auth)
	ctx := context.Background()

	s, _ := m.Create(ctx, Tokens{IDToken: "tok", RefreshToken: "ref-x", Expiry: clock.now().Add(time.Hour)})
	if err := m.Logout(ctx, s.ID); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if auth.revokeCalls.Load() != 1 {
		t.Errorf("revoke calls = %d, want 1", auth.revokeCalls.Load())
	}
	auth.mu.Lock()
	revoked := auth.lastRevoked
	auth.mu.Unlock()
	if revoked != "ref-x" {
		t.Errorf("revoked %q, want ref-x", revoked)
	}
	if _, err := store.Get(ctx, s.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("session survived logout")
	}
	// Idempotent: a second logout of the same id is a no-op success.
	if err := m.Logout(ctx, s.ID); err != nil {
		t.Errorf("second Logout = %v, want nil", err)
	}
}

func TestPendingStore(t *testing.T) {
	clock := newFakeClock()
	p := NewPendingStore()
	p.now = clock.now

	p.Put("state-1", "verifier-1", "/d/index.md")

	pl, ok := p.Take("state-1")
	if !ok {
		t.Fatal("Take(state-1) = false, want pending login")
	}
	if pl.Verifier != "verifier-1" || pl.ReturnTo != "/d/index.md" {
		t.Errorf("pending = %+v", pl)
	}
	// Single use.
	if _, ok := p.Take("state-1"); ok {
		t.Error("state replay accepted — must be single-use")
	}
	if _, ok := p.Take("never-issued"); ok {
		t.Error("unknown state accepted")
	}

	// Expiry.
	p.Put("state-2", "v", "/")
	clock.advance(11 * time.Minute)
	if _, ok := p.Take("state-2"); ok {
		t.Error("expired state accepted")
	}

	// Sweep.
	p.Put("state-3", "v", "/")
	clock.advance(11 * time.Minute)
	if n := p.Sweep(); n != 1 {
		t.Errorf("Sweep = %d, want 1", n)
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
