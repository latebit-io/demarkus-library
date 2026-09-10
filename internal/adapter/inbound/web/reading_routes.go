package web

import "github.com/labstack/echo/v5"

// readingRoutes registers the reading surfaces: the trail canvas, the
// single-pane document permalinks, the catalog, the hover-card fragment, and
// the world-less 1a redirects. The caller (RoomRoutes) supplies the shared
// middleware, no-store first.
func readingRoutes(e *echo.Echo, handler ReadingHandler, mw ...echo.MiddlewareFunc) {
	e.GET("/", handler.Root, mw...)
	e.GET("/t/*", handler.Trail, mw...)
	e.GET("/w/:world/d/*", handler.Doc, mw...)
	e.GET("/w/:world/tags/:tag", handler.TagPage, mw...)
	e.GET("/w/:world/preview/*", handler.Preview, mw...)
	e.GET("/w/:world/raw/*", handler.RawSource, mw...)
	e.GET("/w/:world/search", handler.Search, mw...)
	e.GET("/w/:world/versions/*", handler.History, mw...)

	e.GET("/d/*", handler.LegacyDoc, mw...)
	e.GET("/search", handler.LegacySearch, mw...)
	e.GET("/versions/*", handler.LegacyHistory, mw...)
}
