package web

import "github.com/labstack/echo/v5"

// ReadingRoutes registers the reading-room routes. Root serves the default doc;
// /d/<path> reads any document in the world. Phase 1 layers the floor directory,
// card catalog (LOOKUP), versions, and login on top.
func ReadingRoutes(e *echo.Echo, handler ReadingHandler, middleware ...echo.MiddlewareFunc) {
	e.GET("/", handler.Root, middleware...)
	e.GET("/d/*", handler.Doc, middleware...)
}
