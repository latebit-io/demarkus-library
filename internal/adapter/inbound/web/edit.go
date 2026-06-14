package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// The cataloging desk's web surface (Phase 3; plans/phase-3-cataloging-desk.md).
// Editing is a focused-pane MODE reached from the margin, not a trail chunk —
// an unsaved draft is client state with no place in a shareable reading trail.
// The editor is htmx-pure (ADR 0003): a textarea whose input posts to a
// server-rendered preview, metadata as dedicated form fields (never a body
// frontmatter fence — the bug R1 papered over), and a plain POST save that
// redirects back to the document. Reachable only behind the turnstile.

// editStatuses is the status: axis vocabulary the form's picker offers
// (ADR 0005 decision 7). "" (no status) is allowed — absent ⇒ draft on read.
var editStatuses = []string{"", "draft", "wip", "accepted", "archived"}

// editVM is the view model of the "edit" template.
type editVM struct {
	Title         string // page <title>
	World         string
	WorldPath     string
	Path          string
	Body          string
	DocTitle      string // metadata title field
	Tags          string // ordinary tags, comma-joined for the input
	Importance    string
	Status        string
	Version       int
	Statuses      []string
	Authenticated bool
	Error         string // conflict / write-error banner; empty ⇒ none
}

// EditForm serves the edit form pre-filled from the document's current source
// and metadata. GET /w/:world/edit/<path>.
func (h *ReadingHandler) EditForm(c *echo.Context) error {
	world := c.Param("world")
	p := "/" + c.Param("*")
	draft, err := h.reading.EditDraft(c.Request().Context(), world, p)
	if err != nil {
		return presentError(c, err, world, p)
	}
	vm := editVM{
		Title:         "Edit: " + p,
		World:         world,
		WorldPath:     url.PathEscape(world),
		Path:          p,
		Body:          draft.Body,
		DocTitle:      draft.Title,
		Tags:          strings.Join(draft.Tags, ", "),
		Importance:    draft.Importance,
		Status:        draft.Status,
		Version:       draft.Version,
		Statuses:      editStatuses,
		Authenticated: c.Get(authedKey) != nil,
	}
	return c.Render(http.StatusOK, "edit", vm)
}

// EditPreview renders the edit buffer to sanitized HTML for the live preview —
// the same renderer the reader uses. POST /w/:world/preview (htmx fragment).
func (h *ReadingHandler) EditPreview(c *echo.Context) error {
	rendered, err := h.reading.Preview(c.FormValue("body"))
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "preview failed")
	}
	return c.HTML(http.StatusOK, rendered.HTML)
}

// SaveEdit publishes the submitted edit, then redirects to the document.
// POST /w/:world/edit/<path>. Metadata travels as form fields → the
// mark_publish metadata object (never a body fence). On a version conflict the
// form re-renders with the submitted content and a reload prompt — the edit is
// never silently lost.
func (h *ReadingHandler) SaveEdit(c *echo.Context) error {
	world := c.Param("world")
	p := "/" + c.Param("*")
	// version is the hidden field the edit form rendered from EditDraft, so a
	// non-integer here is a bug or a tampered request — reject it rather than
	// letting it default to 0 (the create sentinel), which would bypass the
	// conflict guard on an existing document.
	version, err := strconv.Atoi(c.FormValue("version"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid version")
	}
	body := c.FormValue("body")
	meta := domain.PublishMeta{
		Title:      strings.TrimSpace(c.FormValue("title")),
		Tags:       assembleTags(c.FormValue("tags"), c.FormValue("status")),
		Importance: strings.TrimSpace(c.FormValue("importance")),
	}

	_, err = h.reading.Publish(c.Request().Context(), world, p, body, meta, version)
	if err == nil {
		// Real POST (the form opts out of hx-boost), so a 303 is a normal
		// browser redirect back to the freshly written document.
		return c.Redirect(http.StatusSeeOther, docRoute(world, p))
	}

	vm := editVM{
		Title:         "Edit: " + p,
		World:         world,
		WorldPath:     url.PathEscape(world),
		Path:          p,
		Body:          body,
		DocTitle:      meta.Title,
		Tags:          strings.TrimSpace(c.FormValue("tags")),
		Importance:    meta.Importance,
		Status:        c.FormValue("status"),
		Version:       version,
		Statuses:      editStatuses,
		Authenticated: c.Get(authedKey) != nil,
		Error:         editErrorMessage(err),
	}
	return c.Render(editErrorStatus(err), "edit", vm)
}

// assembleTags merges the comma-separated tags input with the status picker into
// the final tag set: ordinary tags (status: axis stripped, in case the input
// carried one) plus a single status:<v> tag when a status is chosen.
func assembleTags(tagsCSV, status string) []string {
	var out []string
	for _, t := range strings.Split(tagsCSV, ",") {
		t = strings.TrimSpace(t)
		if t == "" || strings.HasPrefix(t, "status:") {
			continue
		}
		out = append(out, t)
	}
	if s := strings.TrimSpace(status); s != "" {
		out = append(out, "status:"+s)
	}
	return out
}

// editErrorMessage turns a write error into a reader-facing banner.
func editErrorMessage(err error) string {
	switch {
	case errors.Is(err, domain.ErrConflict):
		return "This document changed since you opened it. Open it again to get the latest, then reapply your edits — your text below is preserved."
	case errors.Is(err, domain.ErrWriteUnsupported):
		return "This world is read-only here — editing is available through the broker, not direct connections."
	case errors.Is(err, domain.ErrUnauthorized):
		return "You're not permitted to write to this document."
	case errors.Is(err, domain.ErrNotFound):
		return "This document no longer exists."
	default:
		return "Save failed. Your text below is preserved — try again."
	}
}

// editErrorStatus maps the write error to the re-rendered form's HTTP status.
func editErrorStatus(err error) int {
	switch {
	case errors.Is(err, domain.ErrConflict):
		return http.StatusConflict
	case errors.Is(err, domain.ErrUnauthorized):
		return http.StatusUnauthorized
	default:
		return http.StatusOK
	}
}
