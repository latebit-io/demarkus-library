package web

import (
	"errors"
	"net/http"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus-library/internal/core/port"
)

// PresencePath is the fragment that refreshes the list of who used the
// knowledge system lately, on the focused floor or activity pane.
const PresencePath = "/presence"

// presenceClock has no day: the broker lists a person for minutes, not days.
const presenceClock = "15:04 UTC"

// PresenceHandler serves the presence fragment.
type PresenceHandler struct {
	presence port.Presence // nil = no broker to ask
}

// whoIsHere lists who is here, for a page or the fragment; nil for a room or
// a knowledge system with no list. A failed list is a note, a refused bearer
// included: a refusal of this one tool must not send the reader to sign in.
func whoIsHere(c *echo.Context, presence port.Presence) *presenceVM {
	if presence == nil {
		return nil
	}
	people, err := presence.Present(c.Request().Context())
	if errors.Is(err, domain.ErrPresenceUnsupported) {
		return nil
	}
	if err != nil {
		c.Logger().Warn("presence list failed", "err", err)
	}
	return &presenceVM{Items: presenceItems(people), Unavailable: err != nil}
}

// showPresence puts who is here on the focused pane when it is the floor or
// the activity pane: the page carries the list, so it reads without JS.
func showPresence(c *echo.Context, presence port.Presence, pane *paneVM) {
	switch {
	case pane.Floor != nil:
		pane.Floor.Presence = whoIsHere(c, presence)
	case pane.Activity != nil:
		pane.Presence = whoIsHere(c, presence)
	}
}

// presenceVM is the people to list, or the note that stands in for them.
type presenceVM struct {
	Items       []presenceItemVM
	Unavailable bool
}

// presenceItemVM is one person, the clients seen for them, and when.
type presenceItemVM struct {
	Person  string
	Clients []string
	At      string // the clock time last seen, UTC
	Stamp   string // the same instant for <time datetime>
}

func presenceRoutes(e *echo.Echo, handler PresenceHandler, mw ...echo.MiddlewareFunc) {
	e.GET(PresencePath, handler.Present, mw...)
}

// Present renders the list again for the section's refresh. No list answers
// 404, which is not swapped (page.html hx-status).
func (h PresenceHandler) Present(c *echo.Context) error {
	// A fragment is no page: a reader landing here, as after signing in, goes home.
	if c.Request().Header.Get("HX-Request") != "true" {
		return c.Redirect(http.StatusSeeOther, "/")
	}
	who := whoIsHere(c, h.presence)
	if who == nil {
		return echo.NewHTTPError(http.StatusNotFound, "no presence list")
	}
	return c.Render(http.StatusOK, "presence-list", who)
}

func presenceItems(people []domain.Presence) []presenceItemVM {
	items := make([]presenceItemVM, 0, len(people))
	for _, p := range people {
		seen := p.Seen.UTC()
		items = append(items, presenceItemVM{Person: p.User, Clients: p.Clients,
			At: seen.Format(presenceClock), Stamp: seen.Format(time.RFC3339)})
	}
	return items
}
