package web

import (
	"errors"
	"net/http"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus-library/internal/core/port"
)

// PresencePath is the fragment listing who used the knowledge system lately,
// fetched by the focused floor or activity pane (templates/floor.html).
const PresencePath = "/presence"

// presenceClock has no day: the broker lists a person for minutes, not days.
const presenceClock = "15:04 UTC"

// PresenceHandler serves the presence fragment.
type PresenceHandler struct {
	presence port.Presence // nil = no broker to ask
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

// Present renders who is here. The fetch is unattended, so a failed list is a
// note with a 200, a refused bearer included: only the turnstile sends a
// reader to sign in, and a refusal of this one tool must not loop them there.
func (h PresenceHandler) Present(c *echo.Context) error {
	// A fragment is no page: a reader landing here, as after signing in, goes home.
	if c.Request().Header.Get("HX-Request") != "true" {
		return c.Redirect(http.StatusSeeOther, "/")
	}
	if h.presence == nil {
		return echo.NewHTTPError(http.StatusNotFound, "presence is off")
	}
	people, err := h.presence.Present(c.Request().Context())
	if errors.Is(err, domain.ErrPresenceUnsupported) {
		// Not swapped (page.html hx-status), so the section stays empty.
		return echo.NewHTTPError(http.StatusNotFound, "the knowledge system keeps no presence list")
	}
	if err != nil {
		c.Logger().Warn("presence list failed", "err", err)
	}
	return c.Render(http.StatusOK, "presence-list", presenceVM{Items: presenceItems(people), Unavailable: err != nil})
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
