package web

import (
	"slices"

	"github.com/labstack/echo/v5"
)

// liveRoutes registers the live streams (LongLived). Each answers 404 when no
// change feed is wired.
func liveRoutes(e *echo.Echo, handler LiveHandler, mw ...echo.MiddlewareFunc) {
	// Last, after the caller's turnstile has put the bearer's expiry in ctx.
	mw = append(slices.Clip(mw), renewBeforeExpiry)
	e.GET(LiveEditionPath, handler.LiveEdition, mw...)
	e.GET(LiveSourcesPath, handler.LiveSources, mw...)
	e.GET(LivePulsePath, handler.LivePulse, mw...)
	e.GET(LiveGraphPath, handler.LiveGraph, mw...)
}
