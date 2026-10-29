package web

import (
	"slices"

	"github.com/labstack/echo/v5"
)

// liveRoutes registers the live-edition and stale-answer streams (LongLived).
// Both answer 404 when no change feed is wired.
func liveRoutes(e *echo.Echo, handler LiveHandler, mw ...echo.MiddlewareFunc) {
	// Last, after the caller's turnstile has put the bearer's expiry in ctx.
	mw = append(slices.Clip(mw), renewBeforeExpiry)
	e.GET(LiveEditionPath, handler.LiveEdition, mw...)
	e.GET(LiveSourcesPath, handler.LiveSources, mw...)
}
