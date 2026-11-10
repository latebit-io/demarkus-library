package web

import "github.com/labstack/echo/v5"

// paletteRoutes registers the command palette's active-search fragments
// (ADR 0006 §3). Without JS /palette redirects to /search.
func paletteRoutes(e *echo.Echo, handler PaletteHandler, mw ...echo.MiddlewareFunc) {
	e.GET("/palette", handler.Palette, mw...)
	e.GET("/palette/text", handler.Text, mw...)
}
