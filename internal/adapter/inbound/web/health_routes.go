package web

import "github.com/labstack/echo/v5"

// HealthRoutes registers the liveness endpoint.
func HealthRoutes(e *echo.Echo, handler HealthHandler) {
	e.GET("/health", handler.Check)
}
