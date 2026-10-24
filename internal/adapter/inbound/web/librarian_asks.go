package web

// The pending-ask store: the htmx ask flow is a POST (the question in the
// body, CSRF'd) followed by an SSE GET (EventSource cannot POST). The GET
// must not carry the question — a query string lands in browser history,
// server/proxy logs, and Referer headers, and a 4KB question can breach
// intermediary URL limits — so the POST parks the accepted ask under a
// short-lived opaque token and the stream URL carries only that. A token
// starts one run; the same token again (hx-sse reconnecting after a drop)
// attaches to it. The store is per-pod memory like the conversations it feeds.

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
	// askAttachTTL keeps a started ask's token for reconnects. Hygiene only:
	// the librarian's Attach decides whether the run is still there, so this
	// stays well past any run bound; a stream that settles forgets its token.
	askAttachTTL = time.Hour
	// maxPendingAsks bounds the store against a flood of POSTs that never
	// connect their stream.
	maxPendingAsks = 1024
)

// pendingAsk is one accepted question parked between POST /a/ask and its
// SSE stream: the question, the conversation it belongs to (the stream must
// present the same session), and the trail context for citation rewriting.
type pendingAsk struct {
	question string // cleared at the first take: the run has it from there
	context  string // the reader's trail context, built at POST time; cleared likewise
	convKey  string
	t        trail
	expires  time.Time
	started  bool // a stream took it; later takes attach
}

// askState is what a take found for a token.
type askState int

const (
	askGone   askState = iota // unknown, expired, or another session's
	askFresh                  // first connect: start the run
	askAttach                 // a reconnect: attach to the run
)

// pendingAsks is the token → ask store.
type pendingAsks struct {
	mu sync.Mutex
	m  map[string]*pendingAsk
}

func newPendingAsks() *pendingAsks {
	return &pendingAsks{m: make(map[string]*pendingAsk)}
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
	p.m[token] = &a
	return token, nil
}

// take claims a token for a stream presenting convKey. The first take
// starts the ask (and extends its life to the run's bound); a later take
// attaches to it. Expired, unknown, or another session's tokens are gone.
func (p *pendingAsks) take(token, convKey string) (pendingAsk, askState) {
	p.mu.Lock()
	defer p.mu.Unlock()
	a, ok := p.m[token]
	if !ok || a.convKey != convKey {
		return pendingAsk{}, askGone
	}
	now := time.Now()
	if now.After(a.expires) {
		delete(p.m, token)
		return pendingAsk{}, askGone
	}
	if a.started {
		return *a, askAttach
	}
	a.started = true
	a.expires = now.Add(askAttachTTL)
	fresh := *a
	a.question, a.context = "", "" // up to 12KB per entry; the run owns them now
	return fresh, askFresh
}

// forget drops a token whose stream settled: nothing is left to attach to.
func (p *pendingAsks) forget(token string) {
	p.mu.Lock()
	delete(p.m, token)
	p.mu.Unlock()
}
