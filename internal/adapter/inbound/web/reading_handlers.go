// Package web is the inbound (driving) adapter: an Echo HTTP surface that drives
// the reading room through the inbound port. It depends on port.ReadingService,
// never on the concrete service or any outbound adapter.
//
// Rendering is SSR-first, htmx-hard (ADR 0003): every response is server-rendered
// HTML. A boosted navigation gets the full "page"; a targeted htmx swap (e.g. the
// search box → #main) gets the "content" fragment. One handler, one template —
// no duplicate render path — and everything degrades without JS.
//
// URL scheme: a document's address is (world, path) — /w/<world>/d/<path>,
// /w/<world>/search, /w/<world>/versions/<path>. The world segment is a
// knowledge-system name or a demarkus host[:port]; following mark:// links
// across worlds is how the distributed knowledge graph is traversed. / serves
// the default world's default document; the world-less 1a routes redirect to
// their world-scoped homes.
package web

import (
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/latebit/demarkus-library/internal/core/domain"
	"github.com/latebit/demarkus-library/internal/core/port"
)

// ReadingHandler serves rendered demarkus documents, listings, catalog searches,
// and edition histories. It depends on the inbound port, not the concrete service.
type ReadingHandler struct {
	reading      port.ReadingService
	defaultWorld string
	defaultDoc   string
}

// NewReadingHandler binds the reading service, the world served at /, and the
// document shown there.
func NewReadingHandler(reading port.ReadingService, defaultWorld, defaultDoc string) ReadingHandler {
	return ReadingHandler{reading: reading, defaultWorld: defaultWorld, defaultDoc: defaultDoc}
}

// page is the view model shared by the "page" layout and the "content" partial.
type page struct {
	Title         string
	Host          string
	Path          string
	Content       template.HTML // sanitized by the markdown adapter, links rewritten here
	World         string        // current world (display)
	WorldPath     string        // current world, path-escaped for URL building
	Query         string        // current catalog query (keeps the search box populated)
	ShowHistory   bool          // show the "editions" affordance (documents only)
	Authenticated bool          // behind the turnstile (broker mode) — shows sign-out
}

// viewOpts carries per-view presentation choices into present.
type viewOpts struct {
	world       string // world the view belongs to
	path        string // path for error messages (the requested resource)
	query       string // current catalog query (keeps the search box populated)
	showHistory bool   // show the "editions" affordance (documents only)
	catalog     bool   // linkify the LOOKUP catalog table's Path column
}

// Root renders the configured default document of the default world.
func (h *ReadingHandler) Root(c *echo.Context) error {
	doc, err := h.reading.Read(c.Request().Context(), h.defaultWorld, h.defaultDoc)
	return h.present(c, doc, err, viewOpts{world: h.defaultWorld, path: h.defaultDoc, showHistory: true})
}

// Doc renders a document, or a directory listing (the stacks) when the path
// ends in a slash. /w/<world>/d/<path>.
func (h *ReadingHandler) Doc(c *echo.Context) error {
	world := c.Param("world")
	p := "/" + c.Param("*")
	if strings.HasSuffix(p, "/") {
		doc, err := h.reading.Browse(c.Request().Context(), world, p)
		return h.present(c, doc, err, viewOpts{world: world, path: p})
	}
	doc, err := h.reading.Read(c.Request().Context(), world, p)
	return h.present(c, doc, err, viewOpts{world: world, path: p, showHistory: true})
}

// Search renders the card catalog (LOOKUP) for the q query in the route's
// world. An empty query falls back to the world's index so clearing the box
// returns you somewhere sensible.
func (h *ReadingHandler) Search(c *echo.Context) error {
	world := c.Param("world")
	q := strings.TrimSpace(c.QueryParam("q"))
	if q == "" {
		doc, err := h.reading.Read(c.Request().Context(), world, h.defaultDoc)
		return h.present(c, doc, err, viewOpts{world: world, path: h.defaultDoc, showHistory: true})
	}
	doc, err := h.reading.Search(c.Request().Context(), world, "/", q)
	return h.present(c, doc, err, viewOpts{world: world, path: "/search", query: q, catalog: true})
}

// History renders the edition history of a document. /w/<world>/versions/<path>.
func (h *ReadingHandler) History(c *echo.Context) error {
	world := c.Param("world")
	p := "/" + c.Param("*")
	doc, err := h.reading.History(c.Request().Context(), world, p)
	return h.present(c, doc, err, viewOpts{world: world, path: p})
}

// LegacyDoc 301s the world-less 1a routes to their world-scoped homes so old
// bookmarks keep working.
func (h *ReadingHandler) LegacyDoc(c *echo.Context) error {
	return h.legacyRedirect(c, "/d/"+c.Param("*"))
}

// LegacySearch redirects /search to the default world's catalog.
func (h *ReadingHandler) LegacySearch(c *echo.Context) error {
	target := "/search"
	if q := c.QueryParam("q"); q != "" {
		target += "?q=" + url.QueryEscape(q)
	}
	return h.legacyRedirect(c, target)
}

// LegacyHistory redirects /versions/<path> to the default world's editions.
func (h *ReadingHandler) LegacyHistory(c *echo.Context) error {
	return h.legacyRedirect(c, "/versions/"+c.Param("*"))
}

func (h *ReadingHandler) legacyRedirect(c *echo.Context, suffix string) error {
	return c.Redirect(http.StatusMovedPermanently, "/w/"+url.PathEscape(h.defaultWorld)+suffix)
}

func (h *ReadingHandler) present(c *echo.Context, doc domain.Document, err error, opts viewOpts) error {
	switch {
	case err == nil:
		content := rewriteLinks(doc.HTML, opts.world, doc.Path)
		if opts.catalog {
			content = linkifyCatalogPaths(content, opts.world)
		}
		vm := page{
			Title:         doc.Title,
			Host:          doc.Source,
			Path:          doc.Path,
			Content:       template.HTML(content), //nolint:gosec // sanitized in the markdown adapter; rewriteLinks/linkify only edit links
			World:         opts.world,
			WorldPath:     url.PathEscape(opts.world),
			Query:         opts.query,
			ShowHistory:   opts.showHistory,
			Authenticated: c.Get(authedKey) != nil, // set by RequireSession in broker mode
		}
		return c.Render(http.StatusOK, h.templateFor(c), vm)
	case errors.Is(err, domain.ErrNotFound):
		return echo.NewHTTPError(http.StatusNotFound, opts.path+": not found")
	case errors.Is(err, domain.ErrUnauthorized):
		return echo.NewHTTPError(http.StatusUnauthorized, opts.path+": not authorized")
	default:
		c.Logger().Error("read failed", "world", opts.world, "path", opts.path, "err", err)
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
