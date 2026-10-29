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
	"github.com/latebit-io/demarkus-library/internal/core/port"
)

// sessionCookie carries the reader's sealed session; HttpOnly, so page script
// never sees the tokens inside (ADR 0004).
const sessionCookie = "demarkus_library_session"

// loginCookie carries one sealed in-flight login from /login/start to the
// callback, scoped to the callback path.
const (
	loginCookie     = "demarkus_library_login"
	loginCookiePath = "/auth/callback"
)

// maxReturnTo bounds a post-login target; a longer one lands on /, keeping the
// login URL and the sealed login cookie small.
const maxReturnTo = 2048

// authCookie is one of the turnstile's HttpOnly, Lax cookies. Lax still rides
// the broker's top-level redirect back to the callback.
type authCookie struct{ name, path string }

var (
	sessionCard  = authCookie{name: sessionCookie, path: "/"}
	pendingLogin = authCookie{name: loginCookie, path: loginCookiePath}
)

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

// LoginStart is one sign-in's server-minted state. Named fields, because all
// three are opaque strings a positional return would let a caller transpose.
type LoginStart struct {
	AuthURL  string
	State    string
	Verifier string
}

// LoginFlow is the slice of the broker OAuth dance the web adapter drives.
// The composition root implements it over the oauth adapter; the web package
// stays free of broker specifics (same pattern as session.Authority).
type LoginFlow interface {
	// Begin mints a fresh state + PKCE verifier and returns them with the
	// broker authorize URL to redirect the reader to.
	Begin(ctx context.Context) (LoginStart, error)
	// Exchange redeems the callback code with the stashed verifier.
	Exchange(ctx context.Context, code, verifier string) (session.Tokens, error)
}

// CookieConfig carries the deployment's cookie posture into the auth surface.
type CookieConfig struct {
	// Secure marks the auth cookies HTTPS-only. True in production;
	// false only for localhost development over plain HTTP.
	Secure bool
}

// RequireSession is the turnstile: requests with a live session pass through
// with the reader's bearer in the request context (for the broker world
// gateway); everything else is sent to /login.
//
// htmx requests get an HX-Redirect instead of a 302 — otherwise htmx would
// swap the login page into the #main fragment of a stale shell.
func RequireSession(mgr *session.Manager, cookies CookieConfig) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			cookie, err := c.Cookie(sessionCookie)
			if err != nil || cookie.Value == "" {
				return redirectToLogin(c)
			}
			live, err := mgr.Resolve(c.Request().Context(), cookie.Value)
			switch {
			case err == nil:
				attachSession(c, live, cookies)
				return next(c)
			case errors.Is(err, session.ErrLoginRequired):
				sessionCard.clear(c)
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

// SessionBearer is the lenient turnstile for public routes that read better
// with a reader's identity (theme assets of a private hub): a live session
// lends its bearer, anything else passes anonymous.
func SessionBearer(mgr *session.Manager) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if cookie, err := c.Cookie(sessionCookie); err == nil && cookie.Value != "" {
				// Best effort, and never a Set-Cookie: these responses are
				// cacheable, and a shared cache could replay one. The next
				// page through the turnstile renews a refreshed card.
				if live, err := mgr.Resolve(c.Request().Context(), cookie.Value); err == nil {
					withBearer(c, live.Session)
				}
			}
			return next(c)
		}
	}
}

// attachSession marks the request as the resolved reader's and hands a
// refreshed session back to the browser.
func attachSession(c *echo.Context, live session.Live, cookies CookieConfig) {
	renewSessionCookie(c, live, cookies.Secure)
	c.Set(authedKey, true)
	if email := emailFromIDToken(live.Session.IDToken); email != "" {
		c.Set(userEmailKey, email)
	}
	withBearer(c, live.Session)
}

// withBearer marks the request as the reader's: the bearer and its expiry for
// outbound calls, and the session id as the core's per-reader key.
func withBearer(c *echo.Context, s session.Session) {
	r := c.Request()
	ctx := bearer.WithExpiry(bearer.WithToken(r.Context(), s.IDToken), s.IDTokenExpiry)
	c.SetRequest(r.WithContext(port.WithReader(ctx, s.ID)))
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
	if raw == "" || len(raw) > maxReturnTo || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return ""
	}
	if strings.Contains(raw, "\\") || strings.Contains(raw, "://") {
		return ""
	}
	return raw
}

// set hands the browser value until expires.
func (k authCookie) set(c *echo.Context, value string, expires time.Time, secure bool) {
	c.SetCookie(&http.Cookie{
		Name:  k.name,
		Value: value,
		Path:  k.path,
		// At least a second: MaxAge 0 would drop the attribute and turn
		// the cookie into a browser-session one.
		MaxAge:   max(int(time.Until(expires).Seconds()), 1),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// clear expires the cookie.
func (k authCookie) clear(c *echo.Context) {
	c.SetCookie(&http.Cookie{
		Name:     k.name,
		Value:    "",
		Path:     k.path,
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// renewSessionCookie re-sets the cookie when a refresh re-sealed the session.
func renewSessionCookie(c *echo.Context, live session.Live, secure bool) {
	if live.Cookie != "" {
		sessionCard.set(c, live.Cookie, live.Expires, secure)
	}
}
