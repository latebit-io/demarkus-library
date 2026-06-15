package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/adapter/bearer"
	"github.com/latebit-io/demarkus-library/internal/adapter/inbound/web/session"
)

// sessionCookie is the browser's only piece of auth state: the opaque session
// id. Tokens stay server-side (ADR 0004).
const sessionCookie = "demarkus_library_session"

// authedKey marks a request that passed the turnstile, so templates can show
// the sign-out affordance without the handlers knowing about auth.
const authedKey = "web.authenticated"

// userEmailKey carries the signed-in identity's email for the nav's "signed in
// as" label, so a reader can see which account they're on (Workspace SSO
// silently picks the browser's default account — easy to be on the wrong one).
const userEmailKey = "web.user-email"

// userEmail returns the signed-in identity's email for the current request, or
// "" when absent (unauthenticated, or the id_token carried no email claim).
func userEmail(c *echo.Context) string {
	if v, ok := c.Get(userEmailKey).(string); ok {
		return v
	}
	return ""
}

// emailFromIDToken reads the email claim from a JWT id_token for display only.
// The broker already verified the token when it minted it; this does NOT
// re-verify — it base64url-decodes the payload to surface the email in the nav.
func emailFromIDToken(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return claims.Email
}

// LoginFlow is the slice of the broker OAuth dance the web adapter drives.
// The composition root implements it over the oauth adapter; the web package
// stays free of broker specifics (same pattern as session.Authority).
type LoginFlow interface {
	// Begin mints a fresh state + PKCE verifier and returns the broker
	// authorize URL to redirect the reader to.
	Begin(ctx context.Context) (authURL, state, verifier string, err error)
	// Exchange redeems the callback code with the stashed verifier.
	Exchange(ctx context.Context, code, verifier string) (session.Tokens, error)
}

// CookieConfig carries the deployment's cookie posture into the auth surface.
type CookieConfig struct {
	// Secure marks the session cookie HTTPS-only. True in production;
	// false only for localhost development over plain HTTP.
	Secure bool
	// TTL is the cookie lifetime, matched to the session store's TTL.
	TTL time.Duration
}

// RequireSession is the turnstile: requests with a live session pass through
// with the reader's bearer in the request context (for the broker world
// gateway); everything else is sent to /login.
//
// htmx requests get an HX-Redirect instead of a 302 — otherwise htmx would
// swap the login page into the #main fragment of a stale shell.
func RequireSession(mgr *session.Manager) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			cookie, err := c.Cookie(sessionCookie)
			if err != nil || cookie.Value == "" {
				return redirectToLogin(c)
			}
			token, err := mgr.Token(c.Request().Context(), cookie.Value)
			switch {
			case err == nil:
				c.Set(authedKey, true)
				if email := emailFromIDToken(token); email != "" {
					c.Set(userEmailKey, email)
				}
				r := c.Request()
				c.SetRequest(r.WithContext(bearer.WithToken(r.Context(), token)))
				return next(c)
			case errors.Is(err, session.ErrLoginRequired):
				clearSessionCookie(c)
				return redirectToLogin(c)
			default:
				// Transient (broker unreachable mid-refresh): the
				// session survives; the reader gets the same 502 a
				// failed read would produce.
				c.Logger().Error("session refresh failed", "err", err)
				return echo.NewHTTPError(http.StatusBadGateway, "reading room is unreachable")
			}
		}
	}
}

// redirectToLogin sends the reader to /login, preserving where they were
// headed so the callback can land them back there.
func redirectToLogin(c *echo.Context) error {
	target := loginRedirectURL(sanitizeReturnTo(c.Request().URL.RequestURI()))
	if c.Request().Header.Get("HX-Request") == "true" {
		// Full-page client-side redirect; the swap target must not
		// receive the login document.
		c.Response().Header().Set("HX-Redirect", target)
		return c.NoContent(http.StatusUnauthorized)
	}
	return c.Redirect(http.StatusFound, target)
}

// sanitizeReturnTo keeps post-login redirects inside the app: a single-slash
// site-relative path or nothing. Anything else ("//evil.example", absolute
// URLs, schemes) is an open-redirect attempt and collapses to "".
func sanitizeReturnTo(raw string) string {
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return ""
	}
	if strings.Contains(raw, "\\") || strings.Contains(raw, "://") {
		return ""
	}
	return raw
}

// setSessionCookie hands the browser its library card: the opaque id only.
func setSessionCookie(c *echo.Context, id string, cfg CookieConfig) {
	c.SetCookie(&http.Cookie{
		Name:     sessionCookie,
		Value:    id,
		Path:     "/",
		MaxAge:   int(cfg.TTL.Seconds()),
		HttpOnly: true,
		Secure:   cfg.Secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// clearSessionCookie expires the cookie on logout or a dead session.
func clearSessionCookie(c *echo.Context) {
	c.SetCookie(&http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}
