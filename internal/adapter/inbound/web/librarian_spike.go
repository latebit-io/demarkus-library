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
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
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
// quic mode).
func LibrarianSpikeRoutes(e *echo.Echo, middleware ...echo.MiddlewareFunc) {
	mw := append([]echo.MiddlewareFunc{noStore}, middleware...)
	e.GET("/a/spike", spikePage, mw...)
	e.GET(LibrarianStreamPath, spikeStream, mw...)
}

// spikePage renders a minimal self-contained page that connects the htmx SSE
// extension to the stream. Deliberately not a reading-room template: the page
// is scaffolding for the spike, and inlining it keeps the throwaway surface
// in one file.
func spikePage(c *echo.Context) error {
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
 <p id="answer" sse-swap="token" hx-swap="beforeend"></p>
 <p id="done" sse-swap="done"></p>
</div>
</body>
</html>`, html.EscapeString(q), url.QueryEscape(q), streamURL)

	return c.HTML(http.StatusOK, page)
}

// spikeStream is the SSE endpoint: trace events, then the question echoed
// word by word as token events, then done. With ?slow=N it instead ticks
// once a second for N seconds — the soak that proves streams outlive
// handlerTimeout and survive proxies/ingress between client and pod.
func spikeStream(c *echo.Context) error {
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
	h := w.Header()
	h.Set(echo.HeaderContentType, "text/event-stream")
	h.Set("Cache-Control", "private, no-store")
	// Ask buffering reverse proxies (nginx-style) to pass frames through.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx := c.Request().Context()
	send := func(event, data string) bool {
		if ctx.Err() != nil {
			return false
		}
		// Data is model/user-derived text headed into an hx-swap target:
		// HTML-escape at the boundary, exactly as the librarian will after
		// its renderer. SSE data frames are single-line here by construction.
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, html.EscapeString(data)); err != nil {
			return false
		}
		flusher.Flush()
		return true
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
