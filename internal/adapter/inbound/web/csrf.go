package web

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
)

// csrfCookie holds the CSRF token. It is distinct from the session cookie and
// HttpOnly: the token is rendered into every form server-side ({{ csrf }}), so
// no client JS ever needs to read the cookie — the double-submit check compares
// the cookie against the submitted form field / header on the server.
const csrfCookie = "demarkus_library_csrf"

// CSRFMiddleware protects state-changing requests (the edit/new/append/preview
// POSTs and the login/logout forms). Echo v5's primary defense is the
// Sec-Fetch-Site header that modern browsers send; the cookie+token is the
// fallback for browsers that don't. TokenLookup accepts the hidden form field
// (_csrf, for the plain forms) and the X-CSRF-Token header (for the htmx
// preview POST, which submits a single field outside the form).
//
// Static assets and the health probe are skipped: they are unauthenticated
// GETs that should stay freely cacheable and never carry a Set-Cookie.
func CSRFMiddleware(secure bool) echo.MiddlewareFunc {
	return middleware.CSRFWithConfig(middleware.CSRFConfig{
		Skipper: func(c *echo.Context) bool {
			p := c.Request().URL.Path
			return strings.HasPrefix(p, "/static/") || p == "/health"
		},
		TokenLookup:    "form:_csrf,header:" + echo.HeaderXCSRFToken,
		ContextKey:     csrfContextKey,
		CookieName:     csrfCookie,
		CookiePath:     "/",
		CookieSecure:   secure,
		CookieHTTPOnly: true,
		CookieSameSite: http.SameSiteLaxMode,
	})
}
