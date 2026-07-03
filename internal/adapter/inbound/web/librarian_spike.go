package web

// The Phase 4 SSE spike (plans/phase-4-ai-librarian.md, build order step 1):
// prove the streaming path — Echo streaming response, the vendored htmx SSE
// extension, the ContextTimeout exemption, and (in the cluster) ingress
// buffering — before the librarian's agent loop exists. The stream is an
// echo: it emits a couple of fake trace events, streams the question back
// word by word as token events, and closes with a done event — the exact
// event vocabulary the librarian will use (plan D4). The spike page and the
// echo body are throwaway; the route shape, headers, and event names are not.

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
	"github.com/latebit-io/demarkus-library/internal/core/port"
)

// LibrarianStreamPath is the SSE endpoint. The composition root exempts it
// from the ContextTimeout middleware — a stream is expected to outlive the
// 30s handler bound; its lifetime is governed by the client connection and
// the stream's own end.
const LibrarianStreamPath = "/a/stream"

// spikeSlowCap bounds the ?slow= soak so a crafted URL cannot pin a
// goroutine for hours; 120s is comfortably past every timeout under test.
const spikeSlowCap = 120

// spikeTokenDelay paces token events so streaming is visible to a human
// watching the spike page; spikeTraceDelay spaces the fake trace lines.
const (
	spikeTokenDelay = 150 * time.Millisecond
	spikeTraceDelay = 300 * time.Millisecond
)

// LibrarianSpikeRoutes registers the spike page and stream behind the same
// turnstile middleware as the reading room (auth in broker mode, open in
// quic mode). With a Librarian wired (the composition root resolved an LLM
// provider) the stream runs real asks; with nil it stays the transport-proof
// echo — the feature-dark posture of plan D6.
func LibrarianSpikeRoutes(e *echo.Echo, lib port.Librarian, middleware ...echo.MiddlewareFunc) {
	mw := append([]echo.MiddlewareFunc{noStore}, middleware...)
	h := &librarianSpikeHandler{lib: lib}
	e.GET("/a/spike", h.page, mw...)
	e.GET(LibrarianStreamPath, h.stream, mw...)
}

// librarianSpikeHandler carries the optional librarian into the spike routes.
type librarianSpikeHandler struct{ lib port.Librarian }

// page renders a minimal self-contained page that connects the htmx SSE
// extension to the stream. Deliberately not a reading-room template: the page
// is scaffolding for the spike, and inlining it keeps the throwaway surface
// in one file.
func (h *librarianSpikeHandler) page(c *echo.Context) error {
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

// stream is the SSE endpoint. With a librarian wired, ?q= is a real ask —
// tokens, trace, the reconciling answer event, done. Without one (or with
// ?slow= / the echo fallback) it is the transport spike: trace events, the
// question echoed word by word, done. ?slow=N ticks once a second for N
// seconds — the soak that proves streams outlive handlerTimeout and survive
// proxies/ingress between client and pod.
func (h *librarianSpikeHandler) stream(c *echo.Context) error {
	q := c.QueryParam("q")
	if q == "" {
		q = "hello"
	}
	slow, _ := strconv.Atoi(c.QueryParam("slow"))
	if slow > spikeSlowCap {
		slow = spikeSlowCap
	}

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
		// HTML-escape at the boundary, exactly as the real pane will after
		// its renderer. Multi-line data (answer markdown, token deltas with
		// paragraph breaks) becomes one data: line per SSE spec line — the
		// extension reassembles them with newlines.
		if _, err := fmt.Fprintf(w, "event: %s\n%s\n\n", event, sseData(html.EscapeString(data))); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	if slow == 0 && h.lib != nil {
		return h.ask(ctx, c, q, send)
	}
	// sleep waits d or reports the client is gone; every event is paced so
	// a disconnect is noticed between frames rather than pinning the loop.
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
				return nil
			}
			if !send("token", fmt.Sprintf("tick %d/%d (%.0fs elapsed) ", i, slow, time.Since(start).Seconds())) {
				return nil
			}
		}
		send("done", fmt.Sprintf("soak survived %ds — stream outlived the handler timeout", slow))
		return nil
	}

	for _, line := range []string{
		fmt.Sprintf("lookup %q", q),
		"open (spike: no world read — echo only)",
	} {
		if !send("trace", line) || !sleep(spikeTraceDelay) {
			return nil
		}
	}
	for word := range strings.FieldsSeq("The library whispers back: " + q) {
		if !send("token", word+" ") || !sleep(spikeTokenDelay) {
			return nil
		}
	}
	send("done", "∎")
	return nil
}

// ask runs one real librarian ask and maps the domain events onto the SSE
// vocabulary. The conversation key is the reader's session cookie (broker
// mode) so a conversation follows its login; quic mode shares one local key.
func (h *librarianSpikeHandler) ask(ctx context.Context, c *echo.Context, q string, send func(event, data string) bool) error {
	key := "local"
	if cookie, err := c.Cookie(sessionCookie); err == nil && cookie.Value != "" {
		key = cookie.Value
	}

	events, err := h.lib.Ask(ctx, key, q)
	if err != nil {
		if errors.Is(err, domain.ErrLibrarianBusy) {
			send("trace", "the librarian is still answering your previous question")
			send("done", "busy")
			return nil
		}
		send("trace", "⚠ "+err.Error())
		send("done", "error")
		return nil
	}
	for ev := range events {
		switch ev.Kind {
		case domain.LibrarianToken:
			send("token", ev.Text)
		case domain.LibrarianTrace:
			send("trace", ev.Text)
		case domain.LibrarianAnswer:
			send("answer", ev.Text)
		case domain.LibrarianError:
			send("trace", "⚠ "+ev.Text)
		case domain.LibrarianDone:
			send("done", "∎")
		}
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
