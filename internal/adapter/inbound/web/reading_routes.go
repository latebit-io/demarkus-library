package web

import "github.com/labstack/echo/v5"

// ReadingRoutes registers the reading-room routes. A document's address is
// (world, path):
//   - /                          default world's default document
//   - /w/:world/d/<path>         a document, or the stacks when path ends in /
//   - /w/:world/search?q=        the card catalog (LOOKUP) in that world
//   - /w/:world/versions/<path>  edition history
//
// The world-less 1a routes (/d, /search, /versions) 301 to the default
// world's equivalents so old bookmarks keep working.
func ReadingRoutes(e *echo.Echo, handler ReadingHandler, middleware ...echo.MiddlewareFunc) {
	e.GET("/", handler.Root, middleware...)
	e.GET("/w/:world/d/*", handler.Doc, middleware...)
	e.GET("/w/:world/search", handler.Search, middleware...)
	e.GET("/w/:world/versions/*", handler.History, middleware...)

	e.GET("/d/*", handler.LegacyDoc, middleware...)
	e.GET("/search", handler.LegacySearch, middleware...)
	e.GET("/versions/*", handler.LegacyHistory, middleware...)
}
