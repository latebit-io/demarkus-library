package web

import (
	"net/http"
	"net/url"

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
	pending  *session.PendingStore
	cookies  CookieConfig
}

// NewAuthHandler wires the login flow, session lifecycle, and cookie posture.
func NewAuthHandler(flow LoginFlow, sessions *session.Manager, pending *session.PendingStore, cookies CookieConfig) AuthHandler {
	return AuthHandler{flow: flow, sessions: sessions, pending: pending, cookies: cookies}
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

// Start begins one sign-in: mint state + PKCE server-side, stash them keyed
// by state, and bounce the reader to the broker. POST — minting login state
// is a side effect, and a form keeps it a deliberate action.
func (h *AuthHandler) Start(c *echo.Context) error {
	authURL, state, verifier, err := h.flow.Begin(c.Request().Context())
	if err != nil {
		c.Logger().Error("login begin failed", "err", err)
		return c.Redirect(http.StatusSeeOther, "/login?err=failed")
	}
	h.pending.Put(state, verifier, sanitizeReturnTo(c.FormValue("return_to")))
	return c.Redirect(http.StatusSeeOther, authURL)
}

// Callback is the broker's authorization-code redirect target. The state must
// match a pending login (single-use, CSRF defense); the code is exchanged with
// the stashed PKCE verifier; success mints the session and sets the cookie.
func (h *AuthHandler) Callback(c *echo.Context) error {
	pl, ok := h.pending.Take(c.QueryParam("state"))
	if !ok {
		// Unknown, expired, or replayed state — start over.
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
	s, err := h.sessions.Create(c.Request().Context(), tokens)
	if err != nil {
		c.Logger().Error("session create failed", "err", err)
		return c.Redirect(http.StatusFound, "/login?err=failed")
	}

	setSessionCookie(c, s.ID, h.cookies)
	target := pl.ReturnTo
	if target == "" {
		target = "/"
	}
	return c.Redirect(http.StatusFound, target)
}

// Logout revokes the session at the broker (best effort), drops it from the
// store, clears the cookie, and lands on the login page. POST via a real
// form — logout is a state change.
func (h *AuthHandler) Logout(c *echo.Context) error {
	if cookie, err := c.Cookie(sessionCookie); err == nil && cookie.Value != "" {
		if err := h.sessions.Logout(c.Request().Context(), cookie.Value); err != nil {
			// Log and continue: the cookie is cleared regardless, and
			// the server-side sweep collects any leftover state.
			c.Logger().Error("logout failed", "err", err)
		}
	}
	clearSessionCookie(c)
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
