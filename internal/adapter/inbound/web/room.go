package web

import (
	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/port"
)

// The reading room's composition: one handler per surface family, all built
// from the same dependencies and registered together. Room is the builder the
// composition root drives; RoomRoutes turns it into routes. Handlers are
// assembled here and nowhere else, so a surface's dependencies are visible in
// one place instead of accumulating on a single handler.

// Room carries what every surface family is built from.
type Room struct {
	reading      port.ReadingService
	lib          port.Librarian // nil = librarian not configured (feature-dark)
	brands       *WorldBrands   // in-world branding resolver (ADR 0008); nil ⇒ manifest only
	asks         *pendingAsks   // POST /a/ask → SSE handoff tokens, shared by both librarian routes
	defaultWorld string
	defaultDoc   string
	terms        Terms // display vocabulary (Branding.Terms); handler-built labels read it
	paneScroll   bool  // the pane-scroll room, ADR 0007 (DEMARKUS_PANE_SCROLL opts out)
}

// NewRoom binds the reading service, the world served at /, and the document
// shown there. Everything else is an optional layer, added With-style.
func NewRoom(reading port.ReadingService, defaultWorld, defaultDoc string) Room {
	return Room{
		reading:      reading,
		asks:         newPendingAsks(),
		defaultWorld: defaultWorld,
		defaultDoc:   defaultDoc,
		terms:        DefaultTerms(),
	}
}

// WithBranding adopts the operator's vocabulary for labels the handlers build
// in Go (floor titles, dock anchor, palette rows); the templates read the same
// Branding through the view.
func (r Room) WithBranding(b Branding) Room {
	if b.Terms.Universe != "" {
		r.terms = b.Terms
	}
	return r
}

// WithLibrarian wires the Phase 4 librarian into the room (the composition
// root calls it when an LLM provider resolved). Without it the librarian pane
// renders its not-on-duty state, the nav door is absent, and asks are rejected.
func (r Room) WithLibrarian(lib port.Librarian) Room {
	r.lib = lib
	return r
}

// WithWorldBrands gives the branding desk the resolver to read current
// branding from and to invalidate after a save.
func (r Room) WithWorldBrands(w *WorldBrands) Room {
	r.brands = w
	return r
}

// WithPaneScroll selects the pane-scroll room (ADR 0007 — the default; the
// composition root skips it only when DEMARKUS_PANE_SCROLL opts out): the
// canvas is a fixed-viewport room where each pane scrolls internally (the
// sliding-panes model). Presentation only — a body class the stylesheet keys
// off; routes, trail state, and the no-JS room are untouched.
func (r Room) WithPaneScroll() Room {
	r.paneScroll = true
	return r
}

// chrome is the nav builder every surface shares (chrome.go).
func (r Room) chrome() chromeBuilder {
	return chromeBuilder{librarianOn: r.lib != nil, paneScroll: r.paneScroll}
}

// spatialPanes is the floor/map/graph pane builder the canvas and the spatial
// permalinks share.
func (r Room) spatialPanes() spatialPanes {
	return spatialPanes{maps: r.reading, graph: r.reading, terms: r.terms}
}

// librarianPanes is the pane builder the canvas and the librarian's own routes
// share, so an answer renders the same in a pane and on the stream.
func (r Room) librarianPanes() librarianPanes {
	return librarianPanes{lib: r.lib, reader: r.reading, defaultWorld: r.defaultWorld, terms: r.terms}
}

// readingHandler builds the reading surfaces: documents, the catalog, and the
// trail canvas that composes every pane kind.
func (r Room) readingHandler() ReadingHandler {
	return ReadingHandler{
		reading:      r.reading,
		spatial:      r.spatialPanes(),
		librarian:    r.librarianPanes(),
		defaultWorld: r.defaultWorld,
		defaultDoc:   r.defaultDoc,
		chrome:       r.chrome(),
		terms:        r.terms,
	}
}

// editHandler builds the cataloging desk over the editor port alone.
func (r Room) editHandler() EditHandler {
	return EditHandler{editor: r.reading, chrome: r.chrome()}
}

// spatialHandler builds the graph and map permalinks.
func (r Room) spatialHandler() SpatialHandler {
	return SpatialHandler{spatialPanes: r.spatialPanes(), chrome: r.chrome()}
}

// paletteHandler builds the command palette over the catalog's name index.
func (r Room) paletteHandler() PaletteHandler {
	return PaletteHandler{index: r.reading, defaultWorld: r.defaultWorld, terms: r.terms}
}

// librarianHandler builds the librarian's request surface. The pending-ask
// store is Room's, so the ask POST and its SSE stream share one.
func (r Room) librarianHandler() LibrarianHandler {
	return LibrarianHandler{librarianPanes: r.librarianPanes(), asks: r.asks}
}

// brandingHandler builds the branding desk over its two-method write port.
func (r Room) brandingHandler() BrandingHandler {
	return BrandingHandler{writer: r.reading, brands: r.brands, chrome: r.chrome()}
}

// RoomRoutes registers every surface family's routes. A document's address is
// (world, path):
//
//   - /                          the default trail (default world's default doc)
//   - /palette                   command palette results fragment, htmx (ADR 0006 §3)
//   - /t/<trail>                 the trail canvas (ADR 0005; format in trail.go)
//   - /u                         the universe map fragment for the floor overlay (ADR 0006 §6)
//   - /w/:world/d/<path>         a document, or the stacks when path ends in /
//   - /w/:world/g/<path>         the graph neighborhood (links + backlinks)
//   - /w/:world/u                the world map (catalog by directory cluster)
//   - /w/:world/branding         the branding desk (ADR 0008)
//   - /w/:world/edit/<path>      the cataloging desk: edit a document (Phase 3)
//   - /w/:world/new              the cataloging desk: create a document (Phase 3)
//   - /w/:world/append/<path>    the cataloging desk: append to a document (Phase 3)
//   - /w/:world/tags/:tag        lookup-backed tag page (the lateral exit)
//   - /w/:world/preview/<path>   hover-card fragment for a document (R3)
//   - /w/:world/raw/<path>       unrendered source — the protocol escape
//   - /w/:world/search?q=        the card catalog (LOOKUP) in that world
//   - /w/:world/versions/<path>  edition history
//   - /a, /a/ask, /a/stream      the AI librarian (Phase 4)
//
// /w/ routes are the stable single-pane permalinks (and what the margin's
// escape block points at); /t/ is where reading happens. The world-less 1a
// routes (/d, /search, /versions) 301 to the default world's equivalents so
// old bookmarks keep working.
func RoomRoutes(e *echo.Echo, room Room, middleware ...echo.MiddlewareFunc) {
	// no-store fronts every room route (dynamic, authed); the caller's
	// turnstile middleware runs after it.
	mw := append([]echo.MiddlewareFunc{noStore}, middleware...)
	readingRoutes(e, room.readingHandler(), mw...)
	editRoutes(e, room.editHandler(), mw...)
	spatialRoutes(e, room.spatialHandler(), mw...)
	paletteRoutes(e, room.paletteHandler(), mw...)
	librarianRoutes(e, room.librarianHandler(), mw...)
	brandingRoutes(e, room.brandingHandler(), mw...)
}

// noStore marks reading-room responses uncacheable by any shared cache. Every
// page here is authenticated and per-session (the floor and trail render from
// the reader's identity), so a shared cache — a browser's bfcache, but
// especially a corporate forward proxy — must never hold one: it is both a
// staleness bug (a proxy kept serving a reader a frozen, half-built floor that
// no incognito window could bypass) and a privacy gap (one reader's authed
// page served to another). Static assets are registered separately
// (StaticRoutes) and stay cacheable.
func noStore(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		c.Response().Header().Set("Cache-Control", "private, no-store")
		return next(c)
	}
}
