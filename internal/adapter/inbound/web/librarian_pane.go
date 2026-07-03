package web

// The librarian pane (Phase 4, plans/phase-4-ai-librarian.md): the reader's
// conversation with the AI librarian as a canvas pane — kind `a`, joining the
// trail so reading and asking share one spatial state. The transcript renders
// server-side from port.Librarian.History; answers are markdown rendered
// through the same pipeline as documents (Preview → rewriteLinks → previewize
// → trailize), so mark:// citations become trail-continuing links.
//
// Asking is a real CSRF'd form POST (ADR 0003): with htmx it returns an
// exchange fragment whose SSE block streams the answer live; without JS the
// handler runs the ask to completion and redirects back to the trail (PRG),
// where the pane re-renders the finished exchange from History.

import (
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// maxQuestionBytes bounds one question (plan D7) — well under the global
// body limit; a question is a question, not a document.
const maxQuestionBytes = 4 * 1024

// librarianAskVM feeds the pane's ask form: the current trail context rides
// as hidden fields so the POST can rebuild post-ask URLs and the stream can
// trailize answer links.
type librarianAskVM struct {
	TrailRest string // the /t/* remainder for the current trail
	Idx       int    // this pane's index on the trail
	Notice    string // busy/error line rendered above the form (no-JS PRG)
}

// librarianExchangeVM renders one transcript exchange; Answer is the
// document-pipeline-rendered HTML. Live exchanges (htmx ask response) carry
// StreamURL instead — the SSE block that fills in as the librarian works.
type librarianExchangeVM struct {
	Question  string
	Answer    template.HTML
	StreamURL string
}

// librarianPaneVM is the pane template's librarian branch data.
type librarianPaneVM struct {
	Enabled   bool
	Exchanges []librarianExchangeVM
	Ask       *librarianAskVM // nil unless the pane is focused and enabled
}

// librarianPaneView builds the librarian pane: transcript from History,
// rendered like any pane body, ask form when focused. No world read, never
// errors — a librarian problem is a notice, not a tombstone.
func (h *ReadingHandler) librarianPaneView(c *echo.Context, t trail, i int) paneVM {
	focused := i == t.Focus
	mode := "spine"
	switch {
	case focused:
		mode = "focused"
	case i == t.Focus-1:
		mode = "body"
	}
	vm := paneVM{
		Mode:     mode,
		Kind:     paneLibrarian,
		FocusURL: trailURL(trailFocused(t, i)),
		Title:    "Librarian",
		World:    "librarian",
	}
	if mode == "spine" {
		return vm
	}

	lp := librarianPaneVM{Enabled: h.lib != nil}
	if h.lib != nil {
		for _, ex := range h.lib.History(conversationKey(c)) {
			lp.Exchanges = append(lp.Exchanges, librarianExchangeVM{
				Question: ex.Question,
				Answer:   h.renderAnswer(ex.Answer, t, i),
			})
		}
		// The ask form rides every EXPANDED librarian pane, focused or not:
		// reading a cited document focuses the doc pane, and that is exactly
		// when the follow-up question comes — losing the ask bar there would
		// force a re-focus round trip. Only the collapsed spine drops it.
		lp.Ask = &librarianAskVM{
			TrailRest: strings.TrimPrefix(trailBasePath(t), "/t/"),
			Idx:       i,
			Notice:    c.QueryParam("notice"),
		}
	}
	vm.Librarian = &lp
	return vm
}

// renderAnswer runs an answer's markdown through the document pipeline:
// render + sanitize (the model's output is untrusted input), resolve links to
// in-app routes, wrap hover previews, and trailize so a cited document
// continues the trail from the librarian pane.
func (h *ReadingHandler) renderAnswer(markdown string, t trail, i int) template.HTML {
	rendered, err := h.reading.Preview(markdown)
	if err != nil {
		// Render failure degrades to escaped plain text — never raw model
		// output into the page.
		return template.HTML("<pre>" + template.HTMLEscapeString(markdown) + "</pre>") //nolint:gosec // explicitly escaped one line up
	}
	content, _ := rewriteLinks(rendered.HTML, h.defaultWorld, "/")
	return template.HTML(trailizeLinks(previewize(content), t, i, false)) //nolint:gosec // sanitized by Preview; the passes only rewrite/wrap links
}

// LibrarianEntrance is GET /a — the door into the librarian: a fresh trail
// holding just the librarian pane. Linkable from anywhere (nav, docs).
func (h *ReadingHandler) LibrarianEntrance(c *echo.Context) error {
	return c.Redirect(http.StatusSeeOther, "/t/"+paneLibrarian)
}

// AskLibrarian is POST /a/ask — the pane's form target. htmx requests get the
// exchange fragment (question + SSE block streaming the answer); plain form
// posts run the ask to completion and redirect back to the trail, where the
// finished exchange renders from History.
func (h *ReadingHandler) AskLibrarian(c *echo.Context) error {
	if h.lib == nil {
		return echo.NewHTTPError(http.StatusNotFound, "the librarian is not on duty")
	}
	question := strings.TrimSpace(c.FormValue("question"))
	if question == "" || len(question) > maxQuestionBytes {
		return echo.NewHTTPError(http.StatusBadRequest, "ask a question (under 4KB)")
	}
	idx, _ := strconv.Atoi(c.FormValue("idx"))

	// The trail rides along for URL-building only (parseTrail clamps a junk
	// idx to a real pane); a junk trail degrades to the bare librarian trail
	// rather than failing the ask.
	t, err := parseTrail(c.FormValue("trail"), strconv.Itoa(idx), "")
	if err != nil || t.Panes[t.Focus].Kind != paneLibrarian {
		t = trail{Panes: []paneAddr{{Kind: paneLibrarian}}, Focus: 0, Reader: -1}
	}

	if c.Request().Header.Get("HX-Request") != "" {
		// htmx: park the ask under a one-shot token and hand back the live
		// exchange; the SSE GET presents the token and starts the run. The
		// question and trail stay out of the URL (history/log/Referer
		// hygiene + URL length limits); the token binds to this session.
		// t carries the CLAMPED focus, so a junk idx can't leak through.
		token, err := h.asks.put(pendingAsk{
			question: question,
			convKey:  conversationKey(c),
			t:        t,
			expires:  time.Now().Add(askTokenTTL),
		})
		if err != nil {
			c.Logger().Error("librarian ask handoff failed", "err", err)
			return echo.NewHTTPError(http.StatusServiceUnavailable, "the librarian is overwhelmed — try again shortly")
		}
		return c.Render(http.StatusOK, "librarian-exchange", librarianExchangeVM{
			Question:  question,
			StreamURL: LibrarianStreamPath + "?ask=" + token,
		})
	}

	// No JS: run the ask synchronously (events drained, tokens unused) and
	// PRG back to the trail — the pane re-renders the answer from History.
	notice := ""
	events, err := h.lib.Ask(c.Request().Context(), conversationKey(c), question)
	switch {
	case errors.Is(err, domain.ErrLibrarianBusy):
		notice = "the librarian is still answering your previous question"
	case err != nil:
		c.Logger().Error("librarian ask failed", "err", err)
		notice = "the librarian could not take that question — try again"
	default:
		sawAnswer := false
		for ev := range events {
			switch ev.Kind {
			case domain.LibrarianAnswer:
				sawAnswer = true
			case domain.LibrarianError:
				c.Logger().Error("librarian run failed", "err", ev.Text)
				notice = "the librarian hit an error answering — the transcript may be incomplete"
			}
		}
		if !sawAnswer && notice == "" {
			// The run ended without an answer (interrupted, or every turn
			// was tool calls) — say so rather than rendering a silent blank.
			notice = "the answer was interrupted — ask again"
		}
	}
	dest := trailURL(t)
	if notice != "" {
		sep := "?"
		if strings.Contains(dest, "?") {
			sep = "&"
		}
		dest += sep + "notice=" + url.QueryEscape(notice)
	}
	return c.Redirect(http.StatusSeeOther, dest)
}

// conversationKey names the reader's conversation: the session cookie in
// broker mode (the conversation follows the login), one shared local key in
// quic mode. The key is server-side only — it never appears in a URL.
func conversationKey(c *echo.Context) string {
	if cookie, err := c.Cookie(sessionCookie); err == nil && cookie.Value != "" {
		return cookie.Value
	}
	return "local"
}
