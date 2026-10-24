package web

// The librarian's SSE surface (Phase 4, plan D4). GET /a/stream speaks the
// stream vocabulary into the pane's htmx SSE block: linked trace lines, the
// answer re-rendered through the document pipeline as it streams, then the
// settled steps, sources, and done. On the wire (htmx 4) the swapping frames
// are unnamed <hx-partial> events aimed at the block's regions; done stays a
// named event (see sseFrame). A real ask arrives only as a token from POST
// /a/ask; the run it starts outlives the connection, so hx-sse's reconnect
// after a drop attaches to it and replays it. ?slow= is the one survivor of
// the transport spike that proved this path (build order step 1): a
// once-a-second soak, kept as the operational diagnostic for the
// timeout/proxy/ingress questions every new environment re-asks.

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// LibrarianStreamPath is the SSE endpoint and LibrarianAskPath the pane's
// form target. Both hold a run subscription for the whole answer, so the
// composition root exempts them from the 30s ContextTimeout; the run's own
// bounds live in the librarian (port.Librarian.Ask).
const (
	LibrarianStreamPath = "/a/stream"
	LibrarianAskPath    = "/a/ask"
)

// soakCap bounds the ?slow= soak so a crafted URL cannot pin a goroutine
// for hours; 120s is comfortably past every timeout under test.
const soakCap = 120

// renderInterval paces the streamed answer's re-renders: often enough to read
// as live, rarely enough that an e-ink panel is not repainting every token.
const renderInterval = 300 * time.Millisecond

// keepaliveInterval paces SSE comment frames while the model is silent, so
// proxies with a read timeout (nginx defaults to 60s) keep the stream open.
const keepaliveInterval = 15 * time.Second

// LibrarianStream is the SSE endpoint. A real ask arrives only as a
// pending-ask token from POST /a/ask (?ask=<token>) — the question never
// rides a GET URL — and streams trace, tokens, the reconciling
// answer/rendered events, done; the same token again attaches to the run after
// a dropped connection. ?slow=N is the transport soak (one tick per
// second for N seconds): the diagnostic that proves streams outlive
// handlerTimeout and survive proxies/ingress between client and pod —
// re-run it whenever the deployment topology changes. Anything else has
// nothing to stream and says so.
func (h *LibrarianHandler) LibrarianStream(c *echo.Context) error {
	// An ask costs model tokens and appends to the reader's conversation;
	// EventSource can't carry a CSRF token, so reject cross-site fetches at
	// the metadata level (absent header = older client or direct curl: allow).
	if site := c.Request().Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return echo.NewHTTPError(http.StatusForbidden, "cross-site stream rejected")
	}
	// Clamp both ends: a negative ?slow= must not skid past the branch
	// points below into a silent no-op.
	slow, _ := strconv.Atoi(c.QueryParam("slow"))
	slow = max(0, min(slow, soakCap))
	token := c.QueryParam("ask")

	// Decide whether there is anything to stream BEFORE committing the SSE
	// response — after WriteHeader a plain HTTP error can't be sent.
	if token == "" && slow == 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "nothing to stream — ask through the librarian pane")
	}
	if token != "" && h.lib == nil {
		return echo.NewHTTPError(http.StatusNotFound, "the librarian is not on duty")
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

	out := newSSEWriter(c.Request().Context(), w, flusher.Flush, keepaliveInterval)
	defer out.close()

	// A real ask arrives ONLY as a pending-ask token (POST /a/ask parked it
	// — the question never rides a GET URL). An expired/foreign token still
	// answers in SSE shape: the EventSource gets its close signal instead
	// of an opaque HTTP error.
	if token != "" {
		pa, state := h.asks.take(token, conversationKey(c))
		events, refusal := h.askEvents(c, token, pa, state)
		if refusal != "" {
			// SSE shape even so: the EventSource gets its close signal.
			out.send("trace", traceNote(refusal))
			settle(out)
			return nil
		}
		if state == askAttach {
			out.send("retrace", "") // the attachment replays every step; clear the shown ones
		}
		h.streamAnswer(c, pa.t, events, out)
		if out.ctx.Err() == nil {
			h.asks.forget(token)
		}
		return nil
	}
	streamSoak(slow, out)
	return nil
}

// sseWriter frames one stream and keeps it alive: an SSE comment every
// interval of silence, which clients ignore and proxies count as traffic.
// close must run before the handler returns; later writes are illegal.
type sseWriter struct {
	ctx   context.Context
	mu    sync.Mutex // serializes the handler's frames with the keepalive
	w     io.Writer
	flush func()
	stop  context.CancelFunc
	done  chan struct{}
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
	return s.write(sseFrame(event, markup))
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

// streamSoak is the transport diagnostic: one tick per second for slow
// seconds. Every event is paced through a ctx-aware sleep so a disconnect is
// noticed between frames rather than pinning the loop.
func streamSoak(slow int, out *sseWriter) {
	sleep := func(d time.Duration) bool {
		select {
		case <-out.ctx.Done():
			return false
		case <-time.After(d):
			return true
		}
	}

	out.send("trace", traceNote(fmt.Sprintf("soak: one tick per second for %ds", slow)))
	start := time.Now()
	for i := 1; i <= slow; i++ {
		if !sleep(time.Second) {
			return
		}
		if !out.send("trace", traceNote(fmt.Sprintf("tick %d/%d (%.0fs elapsed)", i, slow, time.Since(start).Seconds()))) {
			return
		}
	}
	out.send("rendered", "<p>"+html.EscapeString(fmt.Sprintf("soak survived %ds — stream outlived the handler timeout", slow))+"</p>")
	settle(out)
}

// askEvents subscribes the stream to its ask: the first stream starts the
// run, a reconnect attaches to it. A refusal is the reader-facing reason why
// there is nothing to stream.
func (h *LibrarianHandler) askEvents(c *echo.Context, token string, pa pendingAsk, state askState) (events <-chan domain.LibrarianEvent, refusal string) {
	ctx := c.Request().Context()
	switch state {
	case askFresh:
		stream, err := h.lib.Ask(ctx, domain.LibrarianAsk{
			ID:           token,
			Conversation: pa.convKey,
			Question:     pa.question,
			Context:      pa.context,
			Persona:      h.identity.persona(ctx),
		})
		if err != nil {
			return nil, askRefusal(c, err)
		}
		return stream, ""
	case askAttach:
		if stream, ok := h.lib.Attach(ctx, pa.convKey, token); ok {
			return stream, ""
		}
		return nil, "lost track of this answer — reload to see it"
	default:
		return nil, "this ask expired — try again"
	}
}

// streamAnswer maps one subscription's events onto the SSE vocabulary; links
// continue the pane's trail. The subscription closes when the run is done or
// the reader's connection drops; the run itself carries on without it.
func (h *LibrarianHandler) streamAnswer(c *echo.Context, t trail, events <-chan domain.LibrarianEvent, out *sseWriter) {
	var steps []librarianStepVM
	var sources []librarianSourceVM
	var cited []domain.Ref      // what the latest render cited; the final message's list wins
	var pending strings.Builder // the message in progress, not yet rendered in full
	var rendered time.Time
	render := func(markdown string) {
		answer, refs := h.renderAnswer(markdown, t, t.Focus)
		cited = refs
		out.send("rendered", string(answer))
		rendered = time.Now()
	}
	for ev := range events {
		switch ev.Kind {
		case domain.LibrarianToken:
			pending.WriteString(ev.Text)
			if time.Since(rendered) >= renderInterval {
				render(pending.String())
			}
		case domain.LibrarianAnswer:
			// The authoritative message: render it whole; the next message's
			// tokens start fresh.
			pending.Reset()
			render(ev.Text)
		case domain.LibrarianTrace:
			step := stepVM(domain.LibrarianStep{Text: ev.Text, Ref: ev.Ref}, t, t.Focus)
			steps = append(steps, step)
			out.send("trace", h.fragment(c, "librarian-step", step))
		case domain.LibrarianSourceOpened:
			sources = append(sources, sourceVM(domain.LibrarianSource{Ref: ev.Ref, Title: ev.Text}, t, t.Focus))
		case domain.LibrarianError:
			c.Logger().Error("librarian run failed", "err", ev.Text)
			step := librarianStepVM{Text: "⚠ the librarian hit an error mid-answer"}
			steps = append(steps, step)
			out.send("trace", h.fragment(c, "librarian-step", step))
		}
	}
	if out.ctx.Err() != nil {
		return
	}
	if pending.Len() > 0 {
		render(pending.String()) // a stopped run ends mid-message
	}
	h.finish(c, out, steps, sourcesVM(sources, cited, t, t.Focus))
}

// finish settles a live exchange into its final markup: steps collapse,
// sources appear, the stream closes.
func (h *LibrarianHandler) finish(c *echo.Context, out *sseWriter, steps []librarianStepVM, sources librarianSourcesVM) {
	out.send("steps", h.fragment(c, "librarian-steps", steps))
	out.send("sources", h.fragment(c, "librarian-sources", sources))
	settle(out)
}

// settle ends a live exchange: the working line and stop button go, and done
// closes the stream (hx-sse:close) so the client does not reconnect to an
// answer it already has.
func settle(out *sseWriter) {
	out.send("settled", "")
	out.send("done", "∎")
}

// traceNote is a plain trace line: text only, no document.
func traceNote(text string) string {
	return "<li>" + html.EscapeString(text) + "</li>"
}

// fragment renders one named template for the stream, so a live exchange
// settles into exactly the markup a reload renders from History. A failure
// is logged and frames nothing: the reload still shows the exchange whole.
func (h *LibrarianHandler) fragment(c *echo.Context, name string, data any) string {
	var b strings.Builder
	if err := c.Echo().Renderer.Render(c, &b, name, data); err != nil {
		c.Logger().Error("librarian fragment render failed", "template", name, "err", err)
		return ""
	}
	return b.String()
}

// sseRegions maps the swapping half of the stream vocabulary onto the
// exchange block's regions. htmx 4 swaps only UNNAMED SSE frames, so each
// of these goes out as an <hx-partial> aimed (relative to the connecting
// block, hence `find`) at its region with its swap style. done is absent: it
// travels as a named event (a DOM event on the block, no swap) that closes
// the stream via hx-sse:close.
var sseRegions = map[string]struct{ target, swap string }{
	"trace":    {"find .ask-trace", "beforeend"},
	"retrace":  {"find .ask-trace", "innerHTML"},
	"rendered": {"find .ask-answer", "innerHTML"},
	"steps":    {"find .ask-steps", "outerHTML"},
	"sources":  {"find .ask-sources", "outerHTML"},
	"settled":  {"find .ask-live", "outerHTML"},
}

// sseFrame renders one complete wire frame (trailing blank line included)
// for an event of the stream vocabulary; payload is already wire-safe HTML.
func sseFrame(event, payload string) string {
	if r, ok := sseRegions[event]; ok {
		return sseData(`<hx-partial hx-target="`+r.target+`" hx-swap="`+r.swap+`">`+payload+`</hx-partial>`) + "\n\n"
	}
	return "event: " + event + "\n" + sseData(payload) + "\n\n"
}

// sseData renders one event payload as SSE data lines: each newline in the
// payload becomes its own `data:` line, which SSE clients rejoin with
// newlines — multi-line frames stay one event.
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
