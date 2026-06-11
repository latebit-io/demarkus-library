package web

import (
	"net/http"

	"github.com/labstack/echo/v5"
)

// HealthHandler answers liveness checks.
type HealthHandler struct{}

// NewHealthHandler builds the health handler.
func NewHealthHandler() HealthHandler {
	return HealthHandler{}
}

// Check returns 200 OK.
func (h *HealthHandler) Check(c *echo.Context) error {
	return c.String(http.StatusOK, "OK")
}
