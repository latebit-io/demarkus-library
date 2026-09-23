package web

import "github.com/labstack/echo/v5"

// pageCSP is the policy every response carries unless a handler sets its own
// (theme assets do, see blobHandler). Branding stylesheets come from documents
// other people wrote, so the policy limits what a stylesheet can reach:
// fonts and @import only from this origin, XHR and SSE only to this origin,
// no plugins, no <base> hijack. Images stay open to https so documents can
// embed pictures; checkCSS closes that channel for stylesheets instead.
// Inline styles are allowed because the login page and the preview anchors
// use them; inline and eval'd script are not, which is why hx-on is unused.
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
