package web

// The librarian's SSE surface (Phase 4, plan D4). GET /a/stream speaks the
// stream vocabulary into the pane's htmx SSE block: linked trace lines, the
// answer re-rendered through the document pipeline as it streams, then the
// settled steps, sources, and done. On the wire (htmx 4) the swapping frames
// are unnamed <hx-partial> events aimed at the block's regions; done stays a
// named event (see sseFrame). A real ask arrives only as a one-shot token
// from POST /a/ask. ?slow= is the one survivor of the transport spike that
// proved this path (build order step 1): a once-a-second soak, kept as the
// operational diagnostic for the timeout/proxy/ingress questions every new
// environment re-asks.

import (
	"context"
	"fmt"
	"html"
	"net/http"
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

// soakCap bounds the ?slow= soak so a crafted URL cannot pin a goroutine
// for hours; 120s is comfortably past every timeout under test.
const soakCap = 120

// renderInterval paces the streamed answer's re-renders: often enough to read
// as live, rarely enough that an e-ink panel is not repainting every token.
const renderInterval = 300 * time.Millisecond

// LibrarianStream is the SSE endpoint. A real ask arrives only as a
// pending-ask token from POST /a/ask (?ask=<token>) — the question never
// rides a GET URL — and streams trace, tokens, the reconciling
// answer/rendered events, done. ?slow=N is the transport soak (one tick per
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

	ctx := c.Request().Context()
	// send frames markup that is already wire-safe: callers escape text
	// (html.EscapeString) and hand over rendered HTML as is.
	send := func(event, markup string) bool {
		if ctx.Err() != nil {
			return false
		}
		if _, err := fmt.Fprint(w, sseFrame(event, markup)); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// A real ask arrives ONLY as a pending-ask token (POST /a/ask parked it
	// — the question never rides a GET URL). An expired/foreign/replayed
	// token still answers in SSE shape: the EventSource gets its close
	// signal instead of an opaque HTTP error.
	if token != "" {
		pa, ok := h.asks.take(token)
		if !ok || pa.convKey != conversationKey(c) {
			send("trace", traceNote("this ask expired — try again"))
			settle(send)
			return nil
		}
		h.streamAsk(ctx, c, pa, send)
		return nil
	}
	streamSoak(ctx, slow, send)
	return nil
}

// streamSoak is the transport diagnostic: one tick per second for slow
// seconds. Every event is paced through a ctx-aware sleep so a disconnect is
// noticed between frames rather than pinning the loop.
func streamSoak(ctx context.Context, slow int, send func(event, data string) bool) {
	sleep := func(d time.Duration) bool {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(d):
			return true
		}
	}

	send("trace", traceNote(fmt.Sprintf("soak: one tick per second for %ds", slow)))
	start := time.Now()
	for i := 1; i <= slow; i++ {
		if !sleep(time.Second) {
			return
		}
		if !send("trace", traceNote(fmt.Sprintf("tick %d/%d (%.0fs elapsed)", i, slow, time.Since(start).Seconds()))) {
			return
		}
	}
	send("rendered", "<p>"+html.EscapeString(fmt.Sprintf("soak survived %ds — stream outlived the handler timeout", slow))+"</p>")
	settle(send)
}

// streamAsk runs one real librarian ask and maps the domain events onto the
// SSE vocabulary. The pending ask carries the pane's trail server-side, so
// every link the stream renders continues the trail from the librarian pane.
func (h *LibrarianHandler) streamAsk(ctx context.Context, c *echo.Context, pa pendingAsk, send func(event, markup string) bool) {
	t := pa.t
	events, err := h.lib.Ask(ctx, domain.LibrarianAsk{
		Conversation: pa.convKey,
		Question:     pa.question,
		Context:      pa.context,
		Persona:      h.identity.persona(ctx),
	})
	if err != nil {
		send("trace", traceNote(askRefusal(c, err)))
		settle(send)
		return
	}

	var steps []librarianStepVM
	var sources []librarianSourceVM
	var pending strings.Builder // the message in progress, not yet rendered in full
	var rendered time.Time
	render := func(markdown string) {
		send("rendered", string(h.renderAnswer(markdown, t, t.Focus)))
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
			send("trace", h.fragment(c, "librarian-step", step))
		case domain.LibrarianSourceOpened:
			sources = append(sources, sourceVM(domain.LibrarianSource{Ref: ev.Ref, Title: ev.Text}, t, t.Focus))
		case domain.LibrarianError:
			c.Logger().Error("librarian run failed", "err", ev.Text)
			step := librarianStepVM{Text: "⚠ the librarian hit an error mid-answer"}
			steps = append(steps, step)
			send("trace", h.fragment(c, "librarian-step", step))
		}
	}
	if pending.Len() > 0 {
		render(pending.String()) // a stopped run ends mid-message
	}
	send("steps", h.fragment(c, "librarian-steps", steps))
	send("sources", h.fragment(c, "librarian-sources", sources))
	settle(send)
}

// settle ends a live exchange: the working line and stop button go, and done
// closes the stream (hx-sse:close) so the client never reconnects to a spent
// one-shot token.
func settle(send func(event, markup string) bool) {
	send("settled", "")
	send("done", "∎")
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
