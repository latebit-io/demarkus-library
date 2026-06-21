// Package web is the inbound (driving) adapter: an Echo HTTP surface that drives
// the reading room through the inbound port. It depends on port.ReadingService,
// never on the concrete service or any outbound adapter.
//
// Rendering is SSR-first, htmx-hard (ADR 0003): every response is server-rendered
// HTML. A boosted navigation gets the full "page"; a targeted htmx swap gets the
// "content" fragment. One handler, one template — no duplicate render path — and
// everything degrades without JS.
//
// URL scheme: a document's address is (world, path) — /w/<world>/d/<path>,
// /w/<world>/tags/<tag>, /w/<world>/raw/<path>, /w/<world>/versions/<path>.
// The world segment is a knowledge-system name or a demarkus host[:port];
// following mark:// links across worlds is how the distributed knowledge graph
// is traversed. / serves the default world's default document; the world-less
// 1a routes redirect to their world-scoped homes.
//
// There is no global search box (ADR 0005 decision 5): lateral navigation is
// tags (the margin's clickable exit to lookup-backed tag pages) and links.
// The catalog endpoint survives at /w/<world>/search for direct use — R2
// folds it into the trail as the catalog pane.
package web

import (
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus-library/internal/core/port"
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

// tagLink is one clickable tag in the margin — the lateral-nav exit to a
// lookup-backed tag page.
type tagLink struct {
	Name string
	URL  string
}

// page is the view model shared by the "page" layout and the "content" partial.
type page struct {
	Title         string
	Host          string
	Path          string
	Content       template.HTML // sanitized by the markdown adapter, links rewritten here
	World         string        // current world (display)
	WorldPath     string        // current world, path-escaped for URL building
	Authenticated bool          // behind the turnstile (broker mode) — shows sign-out
	User          string        // signed-in identity's email for the nav (empty ⇒ not shown)

	// The margin (documents only — listings and catalog views render
	// without one; an empty margin is correct, ADR 0005 decision 8).
	IsDoc      bool
	Status     string // trust badge: draft | wip | accepted | archived | open vocabulary
	Tags       []tagLink
	Properties []domain.Property // parsed body frontmatter, rendered friendly
	Modified   string            // provenance: response metadata, verbatim
	Version    string
	Agent      string
	MarkURL    string       // canonical protocol address — the escape hatch (decision 12)
	ReaderURL  string       // unused on the single-doc permalink (reader mode is a trail lens); kept so doc-meta renders for both VMs
	GraphURL   string       // margin affordance: open this doc's graph neighborhood
	MapURL     string       // margin affordance: open this world's map (zoom level 2)
	EditURL    string       // margin affordance: edit this doc (Phase 3); only when authed
	NewURL     string       // margin affordance: create a doc in this folder (Phase 3); only when authed
	AppendURL  string       // margin affordance: append to this doc (Phase 3); only when authed
	Backlinks  []backlinkVM // "referenced by" — the observed-links map (R3)
}

// backlinkVM is one entry in the margin's "referenced by" block (R3): a
// document observed linking here. URL navigates to it (a trail URL on the
// canvas, a /w/ permalink on the single-doc view); PreviewURL feeds the same
// hover card the outbound body links use (ADR 0005 §margin — one component,
// both directions).
type backlinkVM struct {
	Title      string
	URL        string
	PreviewURL string
}

// viewOpts carries per-view presentation choices into present.
type viewOpts struct {
	world   string // world the view belongs to
	path    string // path for error messages (the requested resource)
	doc     bool   // a real document: render the margin (trust signals)
	catalog bool   // linkify the LOOKUP catalog table's Path column
}

// Root sends the reader to the floor: the universe view as pane zero
// (ADR 0005 decision 4). Reading starts at the map; the first click into a
// world begins the trail.
func (h *ReadingHandler) Root(c *echo.Context) error {
	home := trail{Panes: []paneAddr{{Kind: paneFloor}}, Focus: 0}
	return c.Redirect(http.StatusFound, trailURL(home))
}

// canvasTrailURL returns the canonical /t/ trail URL for a single pane, or ""
// when the request is a bare htmx fragment (an overlay load, a preview, an
// active-search) that must keep rendering the fragment it asked for. A plain
// browser navigation — or an hx-boosted one — resolves to the trail so
// interactive browsing always lands on the multi-pane canvas instead of a
// standalone centered permalink page. The permalink (/w/<world>/d|u|g|tags/...)
// is otherwise a one-way trap: its own links are permalinks too, so one stray
// click strands the reader in the centered projection. The permalink URL stays
// shareable (a recipient simply follows this redirect) and no-JS still works
// (the canvas is server-rendered). raw/versions keep rendering standalone — they
// are deliberate escapes to the unrendered projection, not navigation surfaces.
func canvasTrailURL(c *echo.Context, addr paneAddr) string {
	hdr := c.Request().Header
	if hdr.Get("HX-Request") == "true" && hdr.Get("HX-Boosted") != "true" {
		return ""
	}
	return trailURL(trail{Panes: []paneAddr{addr}, Focus: 0})
}

// Doc renders a document, or a directory listing (the stacks) when the path
// ends in a slash. /w/<world>/d/<path>.
func (h *ReadingHandler) Doc(c *echo.Context) error {
	world := c.Param("world")
	p := "/" + c.Param("*")
	if u := canvasTrailURL(c, paneAddr{Kind: paneDoc, World: world, Value: p}); u != "" {
		return c.Redirect(http.StatusSeeOther, u)
	}
	doc, err := h.reading.Open(c.Request().Context(), world, p)
	return h.present(c, doc, err, viewOpts{world: world, path: p, doc: !domain.IsListingPath(p)})
}

// Search renders the card catalog (LOOKUP) for the q query in the route's
// world. An empty query falls back to the world's index so a bare /search
// returns you somewhere sensible.
func (h *ReadingHandler) Search(c *echo.Context) error {
	world := c.Param("world")
	q := strings.TrimSpace(c.QueryParam("q"))
	if q == "" {
		doc, err := h.reading.Read(c.Request().Context(), world, h.defaultDoc)
		return h.present(c, doc, err, viewOpts{world: world, path: h.defaultDoc, doc: true})
	}
	doc, err := h.reading.Search(c.Request().Context(), world, "/", q)
	return h.present(c, doc, err, viewOpts{world: world, path: "/search", catalog: true})
}

// TagPage renders the catalog filtered to one tag — the margin's lateral-nav
// destination. /w/<world>/tags/<tag>.
func (h *ReadingHandler) TagPage(c *echo.Context) error {
	world := c.Param("world")
	tag := c.Param("tag")
	if dec, err := url.PathUnescape(tag); err == nil {
		tag = dec
	}
	if u := canvasTrailURL(c, paneAddr{Kind: paneTag, World: world, Value: tag}); u != "" {
		return c.Redirect(http.StatusSeeOther, u)
	}
	doc, err := h.reading.Tag(c.Request().Context(), world, tag)
	return h.present(c, doc, err, viewOpts{world: world, path: "/tags/" + tag, catalog: true})
}

// RawSource serves the document's unrendered markdown — the projection's
// escape to protocol (ADR 0005 decision 12). /w/<world>/raw/<path>.
func (h *ReadingHandler) RawSource(c *echo.Context) error {
	world := c.Param("world")
	p := "/" + c.Param("*")
	raw, err := h.reading.Raw(c.Request().Context(), world, p)
	if err != nil {
		return presentError(c, err, world, p)
	}
	return c.Blob(http.StatusOK, "text/plain; charset=utf-8", []byte(raw.Body))
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
	if err != nil {
		return presentError(c, err, opts.world, opts.path)
	}
	content, edges := rewriteLinks(doc.HTML, opts.world, doc.Path)
	if opts.catalog {
		content = linkifyCatalogPaths(content, opts.world)
	}
	if domain.IsListingPath(opts.path) {
		// Rich index (ADR 0006 §5): enrich the bare ls with catalog title/status/
		// orphan. Runs while hrefs are still /w/ doc routes.
		content = h.richIndex(c.Request().Context(), opts.world, content)
	}
	content = previewize(content)
	if opts.doc {
		// Feed the observed-links map (R3): only real documents are edge
		// sources — listings and catalog views are not.
		h.reading.RecordLinks(opts.world, doc.Path, edges)
	}
	vm := page{
		Title:         doc.Title,
		Host:          doc.Source,
		Path:          doc.Path,
		Content:       template.HTML(content), //nolint:gosec // sanitized in the markdown adapter; rewriteLinks/linkify only edit links
		World:         opts.world,
		WorldPath:     url.PathEscape(opts.world),
		Authenticated: c.Get(authedKey) != nil, // set by RequireSession in broker mode
		User:          userEmail(c),
	}
	if opts.doc {
		vm.IsDoc = true
		vm.Status = doc.Status
		vm.Tags = tagLinks(opts.world, doc.Tags)
		vm.Properties = doc.Properties
		vm.Modified = doc.Modified
		vm.Version = doc.Version
		vm.Agent = doc.Agent
		vm.MarkURL = "mark://" + opts.world + doc.Path
		// The single-doc permalink view is not a trail, so its backlinks and
		// graph affordance point at /w/ permalinks rather than trail URLs.
		vm.GraphURL = "/w/" + vm.WorldPath + "/g" + doc.Path
		vm.MapURL = "/w/" + vm.WorldPath + "/u"
		// Edit affordance only behind the turnstile (writes need an identity);
		// in tokenless QUIC mode it stays hidden — writes are unsupported there.
		if vm.Authenticated {
			vm.EditURL = "/w/" + vm.WorldPath + "/edit" + doc.Path
			vm.NewURL = "/w/" + vm.WorldPath + "/new?dir=" + url.QueryEscape(dirOf(doc.Path))
			vm.AppendURL = "/w/" + vm.WorldPath + "/append" + doc.Path
		}
		vm.Backlinks = backlinkLinks(h.reading.Backlinks(opts.world, doc.Path), func(r domain.Ref) string {
			return docRoute(r.World, r.Path)
		})
	}
	return c.Render(http.StatusOK, h.templateFor(c), vm)
}

// presentError maps domain errors to HTTP errors — shared by the rendered
// views and the raw-source escape.
func presentError(c *echo.Context, err error, world, path string) error {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return echo.NewHTTPError(http.StatusNotFound, path+": not found")
	case errors.Is(err, domain.ErrUnauthorized):
		// An authed request whose bearer the broker rejects mid-session (token
		// revoked between the turnstile's refresh and this read) should re-login,
		// not dead-end on a 401 — the turnstile's own promise, honored here on
		// the read path too. The dead session is cleared so re-login starts
		// clean. Only when a session exists (broker mode): in QUIC mode there is
		// no login route, so a private-path rejection stays a plain 401.
		if c.Get(authedKey) != nil {
			clearSessionCookie(c)
			return redirectToLogin(c)
		}
		return echo.NewHTTPError(http.StatusUnauthorized, path+": not authorized")
	default:
		c.Logger().Error("read failed", "world", world, "path", path, "err", err)
		return echo.NewHTTPError(http.StatusBadGateway, "reading room is unreachable")
	}
}

// tagLinks builds the margin's clickable tag list. The status: axis is
// carried by the badge, not the tag list — repeating it would stuff the
// margin with what the badge already says.
func tagLinks(world string, tags []string) []tagLink {
	worldPath := url.PathEscape(world)
	out := make([]tagLink, 0, len(tags))
	for _, t := range tags {
		if strings.HasPrefix(t, "status:") {
			continue
		}
		out = append(out, tagLink{Name: t, URL: "/w/" + worldPath + "/tags/" + url.PathEscape(t)})
	}
	return out
}

// templateFor returns the fragment for a targeted htmx swap and the full page
// otherwise. A boosted navigation wants the whole document, so it gets the
// full page; only non-boosted htmx requests get the bare fragment.
func (h *ReadingHandler) templateFor(c *echo.Context) string {
	r := c.Request()
	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Boosted") != "true" {
		return "content"
	}
	return "page"
}
