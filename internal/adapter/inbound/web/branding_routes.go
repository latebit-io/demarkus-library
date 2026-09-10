package web

import "github.com/labstack/echo/v5"

// brandingRoutes registers the branding desk (ADR 0008). Both routes sit
// behind the same turnstile as the cataloging desk: a save is a publish.
func brandingRoutes(e *echo.Echo, handler BrandingHandler, mw ...echo.MiddlewareFunc) {
	e.GET("/w/:world/branding", handler.BrandingForm, mw...)
	e.POST("/w/:world/branding", handler.SaveBranding, mw...)
}
