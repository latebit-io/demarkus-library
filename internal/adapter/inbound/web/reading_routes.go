package web

import "github.com/labstack/echo/v5"

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

// ReadingRoutes registers the reading-room routes. A document's address is
// (world, path):
//   - /                          the default trail (default world's default doc)
//   - /t/<trail>                 the trail canvas (ADR 0005; format in trail.go)
//   - /w/:world/d/<path>         a document, or the stacks when path ends in /
//   - /w/:world/g/<path>         the graph neighborhood (links + backlinks)
//   - /w/:world/u                the world map (catalog by directory cluster)
//   - /w/:world/edit/<path>      the cataloging desk: edit a document (Phase 3)
//   - /w/:world/new              the cataloging desk: create a document (Phase 3)
//   - /w/:world/append/<path>    the cataloging desk: append to a document (Phase 3)
//   - /w/:world/tags/:tag        lookup-backed tag page (the lateral exit)
//   - /w/:world/preview/<path>   hover-card fragment for a document (R3)
//   - /w/:world/raw/<path>       unrendered source — the protocol escape
//   - /w/:world/search?q=        the card catalog (LOOKUP) in that world
//   - /w/:world/versions/<path>  edition history
//
// /w/ routes are the stable single-pane permalinks (and what the margin's
// escape block points at); /t/ is where reading happens. The world-less 1a
// routes (/d, /search, /versions) 301 to the default world's equivalents so
// old bookmarks keep working.
func ReadingRoutes(e *echo.Echo, handler ReadingHandler, middleware ...echo.MiddlewareFunc) {
	// no-store fronts every reading route (dynamic, authed); the caller's
	// turnstile middleware runs after it.
	mw := append([]echo.MiddlewareFunc{noStore}, middleware...)
	e.GET("/", handler.Root, mw...)
	e.GET("/t/*", handler.Trail, mw...)
	e.GET("/w/:world/d/*", handler.Doc, mw...)
	e.GET("/w/:world/g/*", handler.GraphPage, mw...)
	e.GET("/w/:world/u", handler.WorldMapPage, mw...)
	e.GET("/w/:world/edit/*", handler.EditForm, mw...)
	e.POST("/w/:world/edit/*", handler.SaveEdit, mw...)
	e.GET("/w/:world/new", handler.NewForm, mw...)
	e.POST("/w/:world/new", handler.CreateDoc, mw...)
	e.GET("/w/:world/append/*", handler.AppendForm, mw...)
	e.POST("/w/:world/append/*", handler.AppendDoc, mw...)
	e.POST("/w/:world/preview", handler.EditPreview, mw...)
	e.GET("/w/:world/tags/:tag", handler.TagPage, mw...)
	e.GET("/w/:world/preview/*", handler.Preview, mw...)
	e.GET("/w/:world/raw/*", handler.RawSource, mw...)
	e.GET("/w/:world/search", handler.Search, mw...)
	e.GET("/w/:world/versions/*", handler.History, mw...)

	e.GET("/d/*", handler.LegacyDoc, mw...)
	e.GET("/search", handler.LegacySearch, mw...)
	e.GET("/versions/*", handler.LegacyHistory, mw...)
}
