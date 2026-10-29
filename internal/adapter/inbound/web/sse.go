package web

// The SSE plumbing every stream shares: the librarian's answers
// (librarian_stream.go) and the live-edition banner (live.go). Each stream
// owns its regions.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v5"
)

// keepaliveInterval paces SSE comment frames while a stream is silent, so
// proxies with a read timeout (nginx defaults to 60s) keep the stream open.
const keepaliveInterval = 15 * time.Second

// crossSiteStream reports a stream fetched from another site. EventSource
// cannot carry a CSRF token, so streams check fetch metadata instead; an
// absent header (older client, direct curl) is allowed.
func crossSiteStream(c *echo.Context) bool {
	site := c.Request().Header.Get("Sec-Fetch-Site")
	return site != "" && site != "same-origin" && site != "none"
}

// openSSE commits the response as an event stream whose swapping events land
// in regions. Decide everything that needs a plain HTTP error first: after
// this only frames can be sent.
func openSSE(c *echo.Context, regions sseRegions) (*sseWriter, error) {
	w := c.Response()
	flusher, ok := w.(http.Flusher)
	if !ok {
		// echo.Response always implements Flusher; a wrapper that hides it
		// would silently buffer the whole stream, so fail loudly instead.
		return nil, echo.NewHTTPError(http.StatusInternalServerError, "response writer does not support streaming")
	}
	hdr := w.Header()
	hdr.Set(echo.HeaderContentType, "text/event-stream")
	hdr.Set("Cache-Control", "private, no-store")
	// Ask buffering reverse proxies (nginx-style) to pass frames through.
	hdr.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	out := newSSEWriter(c.Request().Context(), w, flusher.Flush, keepaliveInterval)
	out.regions = regions
	return out, nil
}

// sseWriter frames one stream and keeps it alive: an SSE comment every
// interval of silence, which clients ignore and proxies count as traffic.
// close must run before the handler returns; later writes are illegal.
type sseWriter struct {
	ctx     context.Context
	mu      sync.Mutex // serializes the handler's frames with the keepalive
	w       io.Writer
	flush   func()
	regions sseRegions
	stop    context.CancelFunc
	done    chan struct{}
}

func newSSEWriter(ctx context.Context, w io.Writer, flush func(), interval time.Duration) *sseWriter {
	tickCtx, stop := context.WithCancel(ctx)
	s := &sseWriter{ctx: ctx, w: w, flush: flush, stop: stop, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		tick := time.NewTicker(interval)
		defer tick.Stop()
		for {
			select {
			case <-tickCtx.Done():
				return
			case <-tick.C:
				s.write(": keepalive\n\n")
			}
		}
	}()
	return s
}

// send writes markup that is already wire-safe: callers escape text
// (html.EscapeString) and hand over rendered HTML as is.
func (s *sseWriter) send(event, markup string) bool {
	return s.write(s.regions.frame(event, markup))
}

// finish ends the stream for the client: hx-sse:close hears done and closes,
// where a bare disconnect would make EventSource reconnect.
func (s *sseWriter) finish() {
	s.send("done", "∎")
}

func (s *sseWriter) close() {
	s.stop()
	<-s.done
}

func (s *sseWriter) write(frame string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		return false
	}
	if _, err := fmt.Fprint(s.w, frame); err != nil {
		return false
	}
	s.flush()
	return true
}

// sseRegion is where a swapping event lands in the block that connected.
type sseRegion struct{ target, swap string }

// sseRegions maps one stream's swapping events onto regions of its connecting
// block. htmx 4 swaps only UNNAMED frames, so each goes out as an <hx-partial>
// aimed (hence `find`) at its region; an unmapped event such as done stays a
// named event that hx-sse:close hears.
type sseRegions map[string]sseRegion

// frame renders one complete wire frame (trailing blank line included);
// payload is already wire-safe HTML.
func (r sseRegions) frame(event, payload string) string {
	if region, ok := r[event]; ok {
		return sseData(`<hx-partial hx-target="`+region.target+`" hx-swap="`+region.swap+`">`+payload+`</hx-partial>`) + "\n\n"
	}
	return "event: " + event + "\n" + sseData(payload) + "\n\n"
}

// sseNewlines folds every SSE line ending into \n: a bare \r would start a
// line of its own, read as a field rather than data.
var sseNewlines = strings.NewReplacer("\r\n", "\n", "\r", "\n")

// sseData renders one event payload as SSE data lines: each newline in the
// payload becomes its own `data:` line, which SSE clients rejoin with
// newlines — multi-line frames stay one event.
func sseData(payload string) string {
	lines := strings.Split(sseNewlines.Replace(payload), "\n")
	var b strings.Builder
	for _, line := range lines {
		b.WriteString("data: ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return strings.TrimSuffix(b.String(), "\n")
}
