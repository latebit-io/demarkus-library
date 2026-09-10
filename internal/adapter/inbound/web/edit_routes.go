package web

import "github.com/labstack/echo/v5"

// editRoutes registers the cataloging desk (Phase 3). Every route is a write
// surface or its live preview, so all of them sit behind the turnstile the
// caller supplies.
func editRoutes(e *echo.Echo, handler EditHandler, mw ...echo.MiddlewareFunc) {
	e.GET("/w/:world/edit/*", handler.EditForm, mw...)
	e.POST("/w/:world/edit/*", handler.SaveEdit, mw...)
	e.GET("/w/:world/new", handler.NewForm, mw...)
	e.POST("/w/:world/new", handler.CreateDoc, mw...)
	e.GET("/w/:world/append/*", handler.AppendForm, mw...)
	e.POST("/w/:world/append/*", handler.AppendDoc, mw...)
	e.POST("/w/:world/preview", handler.EditPreview, mw...)
}
