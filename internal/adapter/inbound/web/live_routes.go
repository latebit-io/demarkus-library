package web

import "github.com/labstack/echo/v5"

// liveRoutes registers the live-edition stream (LongLived). It answers 404
// when no change feed is wired.
func liveRoutes(e *echo.Echo, handler LiveHandler, mw ...echo.MiddlewareFunc) {
	e.GET(LiveEditionPath, handler.LiveEdition, mw...)
}
