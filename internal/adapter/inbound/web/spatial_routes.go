package web

import "github.com/labstack/echo/v5"

// spatialRoutes registers the graph and map permalinks: the universe floor's
// overlay fragment, a document's graph neighborhood, and a world's map.
func spatialRoutes(e *echo.Echo, handler SpatialHandler, mw ...echo.MiddlewareFunc) {
	e.GET("/u", handler.FloorPage, mw...)
	e.GET("/w/:world/g/*", handler.GraphPage, mw...)
	e.GET("/w/:world/u", handler.WorldMapPage, mw...)
}
