package web

import "github.com/labstack/echo/v5"

// ReadingRoutes registers the reading-room routes. A document's address is
// (world, path):
//   - /                          the default trail (default world's default doc)
//   - /t/<trail>                 the trail canvas (ADR 0005; format in trail.go)
//   - /w/:world/d/<path>         a document, or the stacks when path ends in /
//   - /w/:world/g/<path>         the graph neighborhood (links + backlinks)
//   - /w/:world/u                the world map (catalog by directory cluster)
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
	e.GET("/", handler.Root, middleware...)
	e.GET("/t/*", handler.Trail, middleware...)
	e.GET("/w/:world/d/*", handler.Doc, middleware...)
	e.GET("/w/:world/g/*", handler.GraphPage, middleware...)
	e.GET("/w/:world/u", handler.WorldMapPage, middleware...)
	e.GET("/w/:world/tags/:tag", handler.TagPage, middleware...)
	e.GET("/w/:world/preview/*", handler.Preview, middleware...)
	e.GET("/w/:world/raw/*", handler.RawSource, middleware...)
	e.GET("/w/:world/search", handler.Search, middleware...)
	e.GET("/w/:world/versions/*", handler.History, middleware...)

	e.GET("/d/*", handler.LegacyDoc, middleware...)
	e.GET("/search", handler.LegacySearch, middleware...)
	e.GET("/versions/*", handler.LegacyHistory, middleware...)
}
