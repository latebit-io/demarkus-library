package web

// The librarian's request surface (Phase 4, plans/phase-4-ai-librarian.md):
// the entrance, the pane's ask form, and the SSE stream that carries an
// answer back. Rendering belongs to librarianPanes, which the canvas holds
// too — this handler only takes questions and streams answers.

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// LibrarianHandler serves the three librarian routes. It embeds the pane
// builder so an answer streams back through exactly the render pipeline the
// canvas pane uses.
type LibrarianHandler struct {
	librarianPanes
	asks *pendingAsks // POST /a/ask → SSE handoff tokens (librarian_asks.go)
}

// LibrarianEntrance is GET /a — the door into the librarian: a fresh trail
// holding just the librarian pane. Linkable from anywhere (nav, docs).
func (h *LibrarianHandler) LibrarianEntrance(c *echo.Context) error {
	return c.Redirect(http.StatusSeeOther, "/t/"+paneLibrarian)
}

// AskLibrarian is POST /a/ask — the pane's form target. htmx requests get the
// exchange fragment (question + SSE block streaming the answer); plain form
// posts run the ask to completion and redirect back to the trail, where the
// finished exchange renders from History.
func (h *LibrarianHandler) AskLibrarian(c *echo.Context) error {
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
	// The reader's URL focus (which pane their attention was on when they
	// asked) rides separately from idx — idx addresses the librarian pane
	// for link algebra; focus decides whose text goes into the context.
	focus, _ := strconv.Atoi(c.FormValue("focus"))
	focus = max(0, min(focus, len(t.Panes)-1))
	trailContext := h.trailContext(c.Request().Context(), t, focus)

	if c.Request().Header.Get("HX-Request") != "" {
		// htmx: park the ask under a one-shot token and hand back the live
		// exchange; the SSE GET presents the token and starts the run. The
		// question and trail stay out of the URL (history/log/Referer
		// hygiene + URL length limits); the token binds to this session.
		// t carries the CLAMPED focus, so a junk idx can't leak through.
		token, err := h.asks.put(pendingAsk{
			question: question,
			context:  trailContext,
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
	events, err := h.lib.Ask(c.Request().Context(), conversationKey(c), question, trailContext)
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
