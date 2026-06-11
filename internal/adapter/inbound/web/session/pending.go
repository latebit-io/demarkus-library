package session

import (
	"sync"
	"time"
)

// pendingTTL bounds how long a login attempt may sit between the authorize
// redirect and the callback. Matches the broker's own pending-auth window.
const pendingTTL = 10 * time.Minute

// PendingLogin is one in-flight authorize redirect: the CSRF state that will
// come back on the callback, the PKCE verifier that redeems the code, and
// where to send the reader afterwards.
type PendingLogin struct {
	Verifier  string
	ReturnTo  string
	createdAt time.Time
}

// PendingStore holds in-flight logins keyed by state, server-side — the
// browser never sees the verifier. Entries are single-use: Take consumes.
// Safe for concurrent use.
type PendingStore struct {
	now func() time.Time

	mu      sync.Mutex
	pending map[string]PendingLogin
}

// NewPendingStore creates an empty pending-login store.
func NewPendingStore() *PendingStore {
	return &PendingStore{now: time.Now, pending: make(map[string]PendingLogin)}
}

// Put records a login attempt under its state value.
func (p *PendingStore) Put(state, verifier, returnTo string) {
	p.mu.Lock()
	p.pending[state] = PendingLogin{Verifier: verifier, ReturnTo: returnTo, createdAt: p.now()}
	p.mu.Unlock()
}

// Take consumes the pending login for state. False means unknown, expired,
// or already used — the callback must reject the attempt (CSRF defense:
// state is single-use and only ever minted by our own /login).
func (p *PendingStore) Take(state string) (PendingLogin, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pl, ok := p.pending[state]
	if !ok {
		return PendingLogin{}, false
	}
	delete(p.pending, state)
	if p.now().Sub(pl.createdAt) >= pendingTTL {
		return PendingLogin{}, false
	}
	return pl, true
}

// Sweep drops expired pending logins (abandoned authorize redirects) and
// reports how many went. Run on the same ticker as MemoryStore.Sweep.
func (p *PendingStore) Sweep() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for state, pl := range p.pending {
		if p.now().Sub(pl.createdAt) >= pendingTTL {
			delete(p.pending, state)
			n++
		}
	}
	return n
}
