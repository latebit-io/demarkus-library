package web

// The librarian's SSE surface (Phase 4, plan D4). GET /a/stream speaks the
// stream vocabulary into the pane's htmx SSE block: linked trace lines, the
// answer re-rendered through the document pipeline as it streams, then the
// settled steps, sources, and done. On the wire (htmx 4) the swapping frames
// are unnamed <hx-partial> events aimed at the block's regions; done stays a
// named event (see sseRegions). A real ask arrives only as a token from POST
// /a/ask; the run it starts outlives the connection, so hx-sse's reconnect
// after a drop attaches to it and replays it. ?slow= is the one survivor of
// the transport spike that proved this path (build order step 1): a
// once-a-second soak, kept as the operational diagnostic for the
// timeout/proxy/ingress questions every new environment re-asks.

import (
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
// form target. Both hold a run subscription for the whole answer, so they
// are LongLived; the run's own bounds live in the librarian (port.Librarian.Ask).
const (
	LibrarianStreamPath = "/a/stream"
	LibrarianAskPath    = "/a/ask"
)

// librarianRegions places the stream's events in the librarian's exchange block.
var librarianRegions = sseRegions{
	"trace":    {"find .ask-trace", "beforeend"},
	"retrace":  {"find .ask-trace", "innerHTML"},
	"rendered": {"find .ask-answer", "innerHTML"},
	"steps":    {"find .ask-steps", "outerHTML"},
	"sources":  {"find .ask-sources", "outerHTML"},
	"settled":  {"find .ask-live", "outerHTML"},
	"stale":    {"#librarian-stale", "innerHTML"}, // the pane's slot, outside the exchange
}

// soakCap bounds the ?slow= soak so a crafted URL cannot pin a goroutine
// for hours; 120s is comfortably past every timeout under test.
const soakCap = 120

// renderInterval paces the streamed answer's re-renders: often enough to read
// as live, rarely enough that an e-ink panel is not repainting every token.
const renderInterval = 300 * time.Millisecond

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
	// An ask costs model tokens and appends to the reader's conversation.
	if crossSiteStream(c) {
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

	out, err := openSSE(c, librarianRegions)
	if err != nil {
		return err
	}
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
			out.send("trace", fragment(c, "librarian-step", step))
		case domain.LibrarianSourceOpened:
			sources = append(sources, sourceVM(domain.LibrarianSource{Ref: ev.Ref, Title: ev.Text}, t, t.Focus))
		case domain.LibrarianError:
			c.Logger().Error("librarian run failed", "err", ev.Text)
			step := librarianStepVM{Text: "⚠ the librarian hit an error mid-answer"}
			steps = append(steps, step)
			out.send("trace", fragment(c, "librarian-step", step))
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
// sources appear, the stream closes. The run recorded the exchange before it
// ended, so a refilled stale slot's stream covers this answer too.
func (h *LibrarianHandler) finish(c *echo.Context, out *sseWriter, steps []librarianStepVM, sources librarianSourcesVM) {
	out.send("steps", fragment(c, "librarian-steps", steps))
	out.send("sources", fragment(c, "librarian-sources", sources))
	if url := h.staleSourcesURL(h.lib.History(conversationKey(c))); url != "" {
		out.send("stale", fragment(c, "librarian-stale", url))
	}
	settle(out)
}

// settle ends a live exchange: the working line and stop button go, and done
// closes the stream (hx-sse:close) so the client does not reconnect to an
// answer it already has.
func settle(out *sseWriter) {
	out.send("settled", "")
	out.finish()
}

// traceNote is a plain trace line: text only, no document.
func traceNote(text string) string {
	return "<li>" + html.EscapeString(text) + "</li>"
}
