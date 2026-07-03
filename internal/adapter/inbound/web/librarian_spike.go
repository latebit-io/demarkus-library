package web

// The librarian's SSE surface (Phase 4, plan D4) plus the transport spike
// that proved it (build order step 1). GET /a/stream speaks the stream
// vocabulary — trace lines, token deltas, the reconciling `answer` text and
// `rendered` HTML, done — into the pane's htmx SSE block. Without a wired
// librarian (feature-dark, D6) or with ?slow= it stays the transport spike:
// an echo and a soak that prove streaming through every timeout and proxy.
// The /a/spike page is throwaway scaffolding; the stream endpoint is not.

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// LibrarianStreamPath is the SSE endpoint and LibrarianAskPath the pane's
// form target. The composition root exempts both from the ContextTimeout
// middleware: a stream is expected to outlive the 30s handler bound, and the
// no-JS ask runs the same agent loop synchronously — at 30s the middleware
// would cancel the run mid-answer (silently: a ctx cancel is not an error
// event, so the transcript just ends without an answer). Their lifetimes are
// governed by the client connection and the run's own caps (MaxTurns,
// provider timeouts).
const (
	LibrarianStreamPath = "/a/stream"
	LibrarianAskPath    = "/a/ask"
)

// spikeSlowCap bounds the ?slow= soak so a crafted URL cannot pin a
// goroutine for hours; 120s is comfortably past every timeout under test.
const spikeSlowCap = 120

// spikeTokenDelay paces token events so streaming is visible to a human
// watching the spike page; spikeTraceDelay spaces the fake trace lines.
const (
	spikeTokenDelay = 150 * time.Millisecond
	spikeTraceDelay = 300 * time.Millisecond
)

// SpikePage renders a minimal self-contained page that connects the htmx SSE
// extension to the stream. Deliberately not a reading-room template: the page
// is scaffolding for transport verification (the real surface is the
// librarian pane), and inlining it keeps the throwaway part in one file.
func (h *ReadingHandler) SpikePage(c *echo.Context) error {
	q := c.QueryParam("q")
	if q == "" {
		q = "hello from the reading room"
	}
	values := url.Values{"q": {q}}
	if slow := c.QueryParam("slow"); slow != "" {
		values.Set("slow", slow)
	}
	streamURL := html.EscapeString(LibrarianStreamPath + "?" + values.Encode())

	page := fmt.Sprintf(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>librarian SSE spike</title>
<script src="/static/htmx.min.js"></script>
<script src="/static/sse.min.js"></script>
<style>
 body { font: 16px/1.5 Charter, Georgia, serif; max-width: 60ch; margin: 3rem auto; }
 #trace { color: #777; font-size: 0.85em; font-family: monospace; }
 #answer span { margin-right: 0.25em; }
</style>
</head>
<body>
<h1>librarian SSE spike</h1>
<form method="get" action="/a/spike">
 <input name="q" value="%s" size="40" autofocus>
 <button>ask</button>
 <a href="/a/spike?q=%s&amp;slow=45">45s soak</a>
</form>
<div hx-ext="sse" sse-connect="%s" sse-close="done">
 <div id="trace" sse-swap="trace" hx-swap="beforeend"></div>
 <p id="answer" style="white-space: pre-wrap" sse-swap="token" hx-swap="beforeend"></p>
 <p id="final" style="white-space: pre-wrap" sse-swap="answer" hx-swap="innerHTML" hidden></p>
 <p id="done" sse-swap="done"></p>
</div>
</body>
</html>`, html.EscapeString(q), url.QueryEscape(q), streamURL)

	return c.HTML(http.StatusOK, page)
}

// LibrarianStream is the SSE endpoint. A real ask arrives only as a
// pending-ask token from POST /a/ask (?ask=<token>) — the question never
// rides a GET URL — and streams trace, tokens, the reconciling
// answer/rendered events, done. Everything else (?q=, ?slow=) is the
// transport spike: the question echoed word by word, or the once-a-second
// soak that proves streams outlive handlerTimeout and survive
// proxies/ingress between client and pod.
func (h *ReadingHandler) LibrarianStream(c *echo.Context) error {
	// An ask costs model tokens and appends to the reader's conversation;
	// EventSource can't carry a CSRF token, so reject cross-site fetches at
	// the metadata level (absent header = older client or direct curl: allow).
	if site := c.Request().Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return echo.NewHTTPError(http.StatusForbidden, "cross-site stream rejected")
	}
	q := c.QueryParam("q")
	if q == "" {
		q = "hello"
	}
	// Clamp both ends: a negative ?slow= must not skid past the branch
	// points below into a silent no-op echo.
	slow, _ := strconv.Atoi(c.QueryParam("slow"))
	slow = max(0, min(slow, spikeSlowCap))

	w := c.Response()
	flusher, ok := w.(http.Flusher)
	if !ok {
		// echo.Response always implements Flusher; a wrapper that hides it
		// would silently buffer the whole stream, so fail loudly instead.
		return echo.NewHTTPError(http.StatusInternalServerError, "response writer does not support streaming")
	}
	hdr := w.Header()
	hdr.Set(echo.HeaderContentType, "text/event-stream")
	hdr.Set("Cache-Control", "private, no-store")
	// Ask buffering reverse proxies (nginx-style) to pass frames through.
	hdr.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx := c.Request().Context()
	send := func(event, data string) bool {
		if ctx.Err() != nil {
			return false
		}
		// Data is model/user-derived text headed into an hx-swap target:
		// HTML-escape at the boundary. Multi-line payloads (answer markdown,
		// token deltas with paragraph breaks) become one data: line per SSE
		// spec line — the extension reassembles them with newlines.
		if _, err := fmt.Fprintf(w, "event: %s\n%s\n\n", event, sseData(html.EscapeString(data))); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	// sendHTML frames already-safe HTML (the rendered answer) — the payload
	// went through the sanitizing render pipeline; escaping it again would
	// show markup as text.
	sendHTML := func(event, markup string) bool {
		if ctx.Err() != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: %s\n%s\n\n", event, sseData(markup)); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// A real ask arrives ONLY as a pending-ask token (POST /a/ask parked it
	// — the question never rides a GET URL). An expired/foreign/replayed
	// token still answers in SSE shape: the EventSource gets its close
	// signal instead of an opaque HTTP error.
	if token := c.QueryParam("ask"); token != "" {
		if h.lib == nil {
			return echo.NewHTTPError(http.StatusNotFound, "the librarian is not on duty")
		}
		pa, ok := h.asks.take(token)
		if !ok || pa.convKey != conversationKey(c) {
			send("trace", "this ask expired — try again")
			send("done", "∎")
			return nil
		}
		return h.streamAsk(ctx, c, pa, send, sendHTML)
	}
	streamSpike(ctx, q, slow, send)
	return nil
}

// streamSpike is the transport spike: the ?slow= soak or the word-by-word
// echo. Every event is paced through a ctx-aware sleep so a disconnect is
// noticed between frames rather than pinning the loop.
func streamSpike(ctx context.Context, q string, slow int, send func(event, data string) bool) {
	sleep := func(d time.Duration) bool {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(d):
			return true
		}
	}

	if slow > 0 {
		send("trace", fmt.Sprintf("soak: one tick per second for %ds", slow))
		start := time.Now()
		for i := 1; i <= slow; i++ {
			if !sleep(time.Second) {
				return
			}
			if !send("token", fmt.Sprintf("tick %d/%d (%.0fs elapsed) ", i, slow, time.Since(start).Seconds())) {
				return
			}
		}
		send("done", fmt.Sprintf("soak survived %ds — stream outlived the handler timeout", slow))
		return
	}

	for _, line := range []string{
		fmt.Sprintf("lookup %q", q),
		"open (spike: no world read — echo only)",
	} {
		if !send("trace", line) || !sleep(spikeTraceDelay) {
			return
		}
	}
	for word := range strings.FieldsSeq("The library whispers back: " + q) {
		if !send("token", word+" ") || !sleep(spikeTokenDelay) {
			return
		}
	}
	send("done", "∎")
}

// streamAsk runs one real librarian ask and maps the domain events onto the
// SSE vocabulary. The pending ask carries the question and the pane's trail
// context (server-side, via the token) so the rendered answer's citations
// continue the trail.
func (h *ReadingHandler) streamAsk(ctx context.Context, c *echo.Context, pa pendingAsk, send, sendHTML func(event, data string) bool) error {
	t := pa.t

	events, err := h.lib.Ask(ctx, pa.convKey, pa.question)
	if err != nil {
		if errors.Is(err, domain.ErrLibrarianBusy) {
			send("trace", "the librarian is still answering your previous question")
		} else {
			// Internal detail (provider errors can carry endpoint/request
			// specifics) stays in the server log; the pane gets a generic
			// line.
			c.Logger().Error("librarian ask failed", "err", err)
			send("trace", "⚠ the librarian could not take that question — try again")
		}
		send("done", "∎")
		return nil
	}
	done := false
	for ev := range events {
		switch ev.Kind {
		case domain.LibrarianToken:
			send("token", ev.Text)
		case domain.LibrarianTrace:
			send("trace", ev.Text)
		case domain.LibrarianAnswer:
			send("answer", ev.Text)
			// The pane swaps this in whole: the answer through the document
			// pipeline, citations as trail-continuing links.
			sendHTML("rendered", string(h.renderAnswer(ev.Text, t, t.Focus)))
		case domain.LibrarianError:
			c.Logger().Error("librarian run failed", "err", ev.Text)
			send("trace", "⚠ the librarian hit an error mid-answer")
		case domain.LibrarianDone:
			done = true
			send("done", "∎")
		}
	}
	if !done {
		// The port may end a stream on Error alone; the EventSource still
		// needs its close signal (sse-close="done") or the connection —
		// exempt from the handler timeout — would dangle.
		send("done", "∎")
	}
	return nil
}

// sseData renders one event payload as SSE data lines: each newline in the
// payload becomes its own `data:` line, which EventSource clients rejoin
// with newlines — multi-line frames stay one event.
func sseData(payload string) string {
	lines := strings.Split(payload, "\n")
	var b strings.Builder
	for _, line := range lines {
		b.WriteString("data: ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return strings.TrimSuffix(b.String(), "\n")
}
