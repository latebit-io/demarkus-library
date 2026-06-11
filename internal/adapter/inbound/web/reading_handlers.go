// Package web is the inbound (driving) adapter: an Echo HTTP surface that drives
// the reading room through the inbound port. It depends on port.ReadingService,
// never on the concrete service or any outbound adapter.
//
// Rendering is SSR-first, htmx-hard (ADR 0003): every response is server-rendered
// HTML. A boosted navigation gets the full "page"; a targeted htmx swap (e.g. the
// search box → #main) gets the "content" fragment. One handler, one template —
// no duplicate render path — and everything degrades without JS.
package web

import (
	"errors"
	"html/template"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/latebit/demarkus-library/internal/core/domain"
	"github.com/latebit/demarkus-library/internal/core/port"
)

// ReadingHandler serves rendered demarkus documents, listings, catalog searches,
// and edition histories. It depends on the inbound port, not the concrete service.
type ReadingHandler struct {
	reading    port.ReadingService
	defaultDoc string
}

// NewReadingHandler binds the reading service and the document shown at /.
func NewReadingHandler(reading port.ReadingService, defaultDoc string) ReadingHandler {
	return ReadingHandler{reading: reading, defaultDoc: defaultDoc}
}

// page is the view model shared by the "page" layout and the "content" partial.
type page struct {
	Title       string
	Host        string
	Path        string
	Content     template.HTML // sanitized by the markdown adapter, links rewritten here
	Query       string        // current catalog query (keeps the search box populated)
	ShowHistory bool          // show the "editions" affordance (documents only)
}

// viewOpts carries per-view presentation choices into present.
type viewOpts struct {
	path        string // path for error messages (the requested resource)
	query       string // current catalog query (keeps the search box populated)
	showHistory bool   // show the "editions" affordance (documents only)
	catalog     bool   // linkify the LOOKUP catalog table's Path column
}

// Root renders the configured default document.
func (h *ReadingHandler) Root(c *echo.Context) error {
	doc, err := h.reading.Read(h.defaultDoc)
	return h.present(c, doc, err, viewOpts{path: h.defaultDoc, showHistory: true})
}

// Doc renders a document, or a directory listing (the stacks) when the path ends
// in a slash. /d/<path>.
func (h *ReadingHandler) Doc(c *echo.Context) error {
	p := "/" + c.Param("*")
	if strings.HasSuffix(p, "/") {
		doc, err := h.reading.Browse(p)
		return h.present(c, doc, err, viewOpts{path: p})
	}
	doc, err := h.reading.Read(p)
	return h.present(c, doc, err, viewOpts{path: p, showHistory: true})
}

// Search renders the card catalog (LOOKUP) for the q query. An empty query falls
// back to the default document so the box clearing returns you home.
func (h *ReadingHandler) Search(c *echo.Context) error {
	q := strings.TrimSpace(c.QueryParam("q"))
	if q == "" {
		doc, err := h.reading.Read(h.defaultDoc)
		return h.present(c, doc, err, viewOpts{path: h.defaultDoc, showHistory: true})
	}
	doc, err := h.reading.Search("/", q)
	return h.present(c, doc, err, viewOpts{path: "/search", query: q, catalog: true})
}

// History renders the edition history of a document. /versions/<path>.
func (h *ReadingHandler) History(c *echo.Context) error {
	p := "/" + c.Param("*")
	doc, err := h.reading.History(p)
	return h.present(c, doc, err, viewOpts{path: p})
}

func (h *ReadingHandler) present(c *echo.Context, doc domain.Document, err error, opts viewOpts) error {
	switch {
	case err == nil:
		content := rewriteLinks(doc.HTML, doc.Path)
		if opts.catalog {
			content = linkifyCatalogPaths(content)
		}
		vm := page{
			Title:       doc.Title,
			Host:        doc.Source,
			Path:        doc.Path,
			Content:     template.HTML(content), //nolint:gosec // sanitized in the markdown adapter; rewriteLinks/linkify only edit links
			Query:       opts.query,
			ShowHistory: opts.showHistory,
		}
		return c.Render(http.StatusOK, h.templateFor(c), vm)
	case errors.Is(err, domain.ErrNotFound):
		return echo.NewHTTPError(http.StatusNotFound, opts.path+": not found")
	case errors.Is(err, domain.ErrUnauthorized):
		return echo.NewHTTPError(http.StatusUnauthorized, opts.path+": not authorized")
	default:
		c.Logger().Error("read failed", "path", opts.path, "err", err)
		return echo.NewHTTPError(http.StatusBadGateway, "reading room is unreachable")
	}
}

// templateFor returns the fragment for a targeted htmx swap (e.g. search → #main)
// and the full page otherwise. A boosted navigation wants the whole document, so
// it gets the full page; only non-boosted htmx requests get the bare fragment.
func (h *ReadingHandler) templateFor(c *echo.Context) string {
	r := c.Request()
	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Boosted") != "true" {
		return "content"
	}
	return "page"
}
