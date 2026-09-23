package web

import "github.com/labstack/echo/v5"

// pageCSP bounds what a branding stylesheet from a document can reach: fonts,
// @import, and XHR same-origin only, no inline or eval'd script (so no hx-on).
// Images stay open to https for document pictures; checkCSS covers that hole.
const pageCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: https:; font-src 'self'; connect-src 'self'; " +
	"object-src 'none'; base-uri 'self'; frame-ancestors 'self'"

// SecurityHeaders sets the page policy before the handler runs, so a handler
// that needs a different one simply overwrites it.
func SecurityHeaders() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			h := c.Response().Header()
			h.Set("Content-Security-Policy", pageCSP)
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "same-origin")
			return next(c)
		}
	}
}
