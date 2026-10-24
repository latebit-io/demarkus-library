package web

import (
	"net/http"
	"net/url"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/adapter/inbound/web/session"
)

// loginErrors maps the err query code to a fixed, server-chosen message —
// never reflected text, so the login page cannot echo attacker content.
var loginErrors = map[string]string{
	"denied": "Sign-in was cancelled or denied.",
	"failed": "Sign-in failed. Please try again.",
}

// AuthHandler drives the turnstile: the login page, the broker redirect, the
// callback exchange, and logout.
type AuthHandler struct {
	flow     LoginFlow
	sessions *session.Manager
	logins   *session.PendingLogins
	cookies  CookieConfig
}

// NewAuthHandler wires the login flow, session lifecycle, and cookie posture.
func NewAuthHandler(flow LoginFlow, sessions *session.Manager, logins *session.PendingLogins, cookies CookieConfig) AuthHandler {
	return AuthHandler{flow: flow, sessions: sessions, logins: logins, cookies: cookies}
}

// loginPage is the login template's view model.
type loginPage struct {
	ReturnTo string
	Error    string
}

// Login renders the login page: a real form that posts to /login/start, so
// the turnstile works with JS disabled (ADR 0003).
func (h *AuthHandler) Login(c *echo.Context) error {
	return c.Render(http.StatusOK, "login", loginPage{
		ReturnTo: sanitizeReturnTo(c.QueryParam("return_to")),
		Error:    loginErrors[c.QueryParam("err")],
	})
}

// Start begins one sign-in: mint state + PKCE server-side, seal them into the
// login cookie, and bounce the reader to the broker. POST — minting login
// state is a side effect, and a form keeps it a deliberate action.
func (h *AuthHandler) Start(c *echo.Context) error {
	start, err := h.flow.Begin(c.Request().Context())
	if err != nil {
		c.Logger().Error("login begin failed", "err", err)
		return c.Redirect(http.StatusSeeOther, "/login?err=failed")
	}
	value, err := h.logins.Seal(session.PendingLogin{
		State:    start.State,
		Verifier: start.Verifier,
		ReturnTo: sanitizeReturnTo(c.FormValue("return_to")),
	})
	if err != nil {
		c.Logger().Error("login seal failed", "err", err)
		return c.Redirect(http.StatusSeeOther, "/login?err=failed")
	}
	pendingLogin.set(c, value, time.Now().Add(session.PendingTTL), h.cookies.Secure)
	return c.Redirect(http.StatusSeeOther, start.AuthURL)
}

// Callback is the broker's authorization-code redirect target. The state must
// match the sealed pending login (CSRF defense); the code is exchanged with
// its PKCE verifier; success mints the session and sets the cookie.
func (h *AuthHandler) Callback(c *echo.Context) error {
	cookie, err := c.Cookie(loginCookie)
	// Whatever happens next, this attempt is spent.
	pendingLogin.clear(c)
	if err != nil || cookie.Value == "" {
		return c.Redirect(http.StatusFound, "/login?err=failed")
	}
	pl, err := h.logins.Open(cookie.Value, c.QueryParam("state"))
	if err != nil {
		// Mismatched, expired, or sealed under another key — start over.
		c.Logger().Warn("login callback rejected", "err", err)
		return c.Redirect(http.StatusFound, "/login?err=failed")
	}
	if c.QueryParam("error") != "" {
		// The reader cancelled at the IdP, or the broker denied.
		return c.Redirect(http.StatusFound, "/login?err=denied")
	}
	code := c.QueryParam("code")
	if code == "" {
		return c.Redirect(http.StatusFound, "/login?err=failed")
	}

	tokens, err := h.flow.Exchange(c.Request().Context(), code, pl.Verifier)
	if err != nil {
		c.Logger().Error("code exchange failed", "err", err)
		return c.Redirect(http.StatusFound, "/login?err=failed")
	}
	live, err := h.sessions.Create(tokens)
	if err != nil {
		c.Logger().Error("session create failed", "err", err)
		return c.Redirect(http.StatusFound, "/login?err=failed")
	}

	sessionCard.set(c, live.Cookie, live.Expires, h.cookies.Secure)
	target := pl.ReturnTo
	if target == "" {
		target = "/"
	}
	return c.Redirect(http.StatusFound, target)
}

// Logout revokes the session's refresh token at the broker, clears the
// cookie, and lands on the login page. POST via a real form — logout is a
// state change.
func (h *AuthHandler) Logout(c *echo.Context) error {
	if cookie, err := c.Cookie(sessionCookie); err == nil && cookie.Value != "" {
		if err := h.sessions.Logout(c.Request().Context(), cookie.Value); err != nil {
			// Log and continue: the reader still leaves; the cookie is
			// cleared regardless.
			c.Logger().Error("logout failed", "err", err)
		}
	}
	sessionCard.clear(c)
	return c.Redirect(http.StatusSeeOther, "/login")
}

// AuthRoutes registers the turnstile surface. These sit OUTSIDE the
// RequireSession middleware — an unauthenticated reader must be able to reach
// every one of them.
func AuthRoutes(e *echo.Echo, h AuthHandler) {
	e.GET("/login", h.Login)
	e.POST("/login/start", h.Start)
	e.GET("/auth/callback", h.Callback)
	e.POST("/logout", h.Logout)
}

// loginRedirectURL builds the /login target preserving return_to.
func loginRedirectURL(returnTo string) string {
	if returnTo == "" || returnTo == "/" {
		return "/login"
	}
	return "/login?return_to=" + url.QueryEscape(returnTo)
}
