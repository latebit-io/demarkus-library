package web

import "github.com/labstack/echo/v5"

// ReadingRoutes registers the reading-room routes:
//   - /            default document
//   - /d/<path>    a document, or a directory listing (the stacks) when path ends in /
//   - /search?q=   the card catalog (LOOKUP)
//   - /versions/<path>  edition history
//
// Phase 1 still to add on top: org login (the broker OAuth turnstile).
func ReadingRoutes(e *echo.Echo, handler ReadingHandler, middleware ...echo.MiddlewareFunc) {
	e.GET("/", handler.Root, middleware...)
	e.GET("/d/*", handler.Doc, middleware...)
	e.GET("/search", handler.Search, middleware...)
	e.GET("/versions/*", handler.History, middleware...)
}
