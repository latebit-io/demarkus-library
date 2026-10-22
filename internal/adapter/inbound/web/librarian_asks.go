package web

// The pending-ask store: the htmx ask flow is a POST (the question in the
// body, CSRF'd) followed by an SSE GET (EventSource cannot POST). The GET
// must not carry the question — a query string lands in browser history,
// server/proxy logs, and Referer headers, and a 4KB question can breach
// intermediary URL limits — so the POST parks the accepted ask under a
// short-lived opaque token and the stream URL carries only that. A token
// starts one run; the same token again (hx-sse reconnecting after a drop)
// resumes it. The store is per-pod memory like the conversations it feeds.

import (
	"context"
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
	// askResumeTTL is how long a started ask stays resumable: the run's
	// own bound plus room for a late reconnect to collect the answer.
	askResumeTTL = askRunTimeout + time.Minute
	// askRunTimeout bounds a run now that it outlives its SSE connection.
	askRunTimeout = 10 * time.Minute
	// reconnectGrace is how long a run keeps working with no stream
	// attached: hx-sse reconnects within seconds; past this the reader left.
	reconnectGrace = 30 * time.Second
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
	// historyLen counts the conversation's exchanges at POST time; a resume
	// watches History for the one this ask appends.
	historyLen int
	expires    time.Time
	started    bool               // a stream took it; later takes resume
	stop       context.CancelFunc // ends the run; nil until the stream holds it
	abandon    *time.Timer        // the grace countdown while no stream is attached
}

// askState is what a take found for a token.
type askState int

const (
	askGone    askState = iota // unknown, expired, or another session's
	askFresh                   // first connect: start the run
	askResumed                 // a reconnect: pick the run back up
)

// pendingAsks is the token → ask store.
type pendingAsks struct {
	mu    sync.Mutex
	m     map[string]*pendingAsk
	grace time.Duration
}

func newPendingAsks() *pendingAsks {
	return &pendingAsks{m: make(map[string]*pendingAsk), grace: reconnectGrace}
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
			if v.abandon != nil {
				v.abandon.Stop()
			}
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
// resumes it and cancels any abandon countdown. Expired, unknown, or
// another session's tokens are gone.
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
		if a.abandon != nil {
			a.abandon.Stop()
			a.abandon = nil
		}
		return *a, askResumed
	}
	a.started = true
	a.expires = now.Add(askResumeTTL)
	return *a, askFresh
}

// hold records the run's cancel so detach can end it.
func (p *pendingAsks) hold(token string, stop context.CancelFunc) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if a, ok := p.m[token]; ok {
		a.stop = stop
	}
}

// detach starts the grace countdown for a token whose stream dropped: the
// run ends when it fires unless a reconnect takes the token first.
func (p *pendingAsks) detach(token string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	a, ok := p.m[token]
	if !ok || a.stop == nil {
		return
	}
	if a.abandon != nil {
		a.abandon.Stop()
	}
	a.abandon = time.AfterFunc(p.grace, a.stop)
}
