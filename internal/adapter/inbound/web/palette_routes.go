package web

import "github.com/labstack/echo/v5"

// paletteRoutes registers the command palette's active-search fragment
// (ADR 0006 §3). Without JS the same route redirects to /search.
func paletteRoutes(e *echo.Echo, handler PaletteHandler, mw ...echo.MiddlewareFunc) {
	e.GET("/palette", handler.Palette, mw...)
}
