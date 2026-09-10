package web

import "github.com/labstack/echo/v5"

// The nav every surface renders. It lives here, embedded rather than repeated,
// so adding a surface inherits the nav instead of re-deriving it.

// librarianDoor is the nav's entrance to the librarian: a fresh trail holding
// just that pane. The canvas replaces it with a trail-carrying door.
const librarianDoor = "/a"

// navChrome is the assembled nav state. Templates reach its fields through the
// view model that embeds it.
type navChrome struct {
	Authenticated bool   // behind the turnstile (broker mode) — shows sign-out
	User          string // signed-in identity's email (empty ⇒ not shown)
	LibrarianURL  string // nav door to the librarian (empty ⇒ not configured)
	PaneScroll    bool   // the pane-scroll room, ADR 0007 (canvas body class)
}

// chromeBuilder holds what the chrome needs beyond the request: whether a
// librarian is wired and which room the composition root selected.
type chromeBuilder struct {
	librarianOn bool
	paneScroll  bool
}

// build assembles the chrome for one request.
func (b chromeBuilder) build(c *echo.Context) navChrome {
	nav := navChrome{
		Authenticated: c.Get(authedKey) != nil, // set by RequireSession in broker mode
		User:          userEmail(c),
		PaneScroll:    b.paneScroll,
	}
	if b.librarianOn {
		nav.LibrarianURL = librarianDoor
	}
	return nav
}
