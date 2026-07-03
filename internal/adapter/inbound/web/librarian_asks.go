package web

// The pending-ask store: the htmx ask flow is a POST (the question in the
// body, CSRF'd) followed by an SSE GET (EventSource cannot POST). The GET
// must not carry the question — a query string lands in browser history,
// server/proxy logs, and Referer headers, and a 4KB question can breach
// intermediary URL limits — so the POST parks the accepted ask under a
// short-lived opaque token and the stream URL carries only that. Tokens are
// one-shot, session-bound, and expire fast; the store is per-pod memory like
// the conversations it feeds.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

const (
	// askTokenTTL is how long an accepted ask waits for its stream to
	// connect — htmx connects immediately; a minute absorbs slow clients.
	askTokenTTL = time.Minute
	// maxPendingAsks bounds the store against a flood of POSTs that never
	// connect their stream.
	maxPendingAsks = 1024
)

// pendingAsk is one accepted question parked between POST /a/ask and its
// SSE stream: the question, the conversation it belongs to (the stream must
// present the same session), and the trail context for citation rewriting.
type pendingAsk struct {
	question string
	context  string // the reader's trail context, built at POST time
	convKey  string
	t        trail
	expires  time.Time
}

// pendingAsks is the token → ask store.
type pendingAsks struct {
	mu sync.Mutex
	m  map[string]pendingAsk
}

func newPendingAsks() *pendingAsks {
	return &pendingAsks{m: make(map[string]pendingAsk)}
}

var errAskFlood = errors.New("too many pending asks")

// put parks an ask and returns its opaque token. Expired entries are swept
// on the way in; a store still at capacity after the sweep rejects rather
// than evicting someone's connected-any-moment ask.
func (p *pendingAsks) put(a pendingAsk) (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf)

	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for k, v := range p.m {
		if now.After(v.expires) {
			delete(p.m, k)
		}
	}
	if len(p.m) >= maxPendingAsks {
		return "", errAskFlood
	}
	p.m[token] = a
	return token, nil
}

// take consumes a token: one shot, gone on first use, dead past its TTL.
func (p *pendingAsks) take(token string) (pendingAsk, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	a, ok := p.m[token]
	if !ok {
		return pendingAsk{}, false
	}
	delete(p.m, token)
	if time.Now().After(a.expires) {
		return pendingAsk{}, false
	}
	return a, true
}
