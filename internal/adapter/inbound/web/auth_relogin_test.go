package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// reloginProbe returns presentError(ErrUnauthorized) from a route, optionally
// marking the request as having passed the turnstile (broker mode).
func reloginProbe(authed bool) *echo.Echo {
	app := echo.New()
	app.GET("/probe", func(c *echo.Context) error {
		if authed {
			c.Set(authedKey, true)
		}
		return presentError(c, domain.ErrUnauthorized, "w", "/secret.md")
	})
	return app
}

// TestPresentError_Unauthorized_AuthedReLogins: a bearer rejected mid-session
// bounces to /login (and clears the dead session) instead of dead-ending on a
// 401 — for both a plain navigation and an htmx request.
func TestPresentError_Unauthorized_AuthedReLogins(t *testing.T) {
	t.Run("plain redirects 302 to login", func(t *testing.T) {
		rec := httptest.NewRecorder()
		reloginProbe(true).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/probe", http.NoBody))
		if rec.Code != http.StatusFound {
			t.Fatalf("status = %d, want 302", rec.Code)
		}
		if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/login") {
			t.Fatalf("Location = %q, want /login...", loc)
		}
		if !sessionCookieCleared(rec) {
			t.Fatal("session cookie not cleared on re-login")
		}
	})

	t.Run("htmx gets HX-Redirect", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/probe", http.NoBody)
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		reloginProbe(true).ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
		if loc := rec.Header().Get("HX-Redirect"); !strings.HasPrefix(loc, "/login") {
			t.Fatalf("HX-Redirect = %q, want /login...", loc)
		}
	})
}

// TestPresentError_Unauthorized_NoSessionStays401: without a turnstile (QUIC
// mode, no login route) a private-path rejection stays a plain 401, never a
// redirect to a route that doesn't exist.
func TestPresentError_Unauthorized_NoSessionStays401(t *testing.T) {
	rec := httptest.NewRecorder()
	reloginProbe(false).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/probe", http.NoBody))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if rec.Header().Get("Location") != "" {
		t.Fatalf("unexpected redirect Location = %q", rec.Header().Get("Location"))
	}
}

func sessionCookieCleared(rec *httptest.ResponseRecorder) bool {
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.MaxAge < 0 {
			return true
		}
	}
	return false
}
