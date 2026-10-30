package web

// The pulse (plans/watch-realtime.md, next 5): the recent writes in the worlds
// this reader may see, on the floor and in the activity pane, swapped in only
// when the list changed, so a write in a hidden world sends nothing.

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// LivePulsePath is the pulse stream; LongLived like the edition one.
const LivePulsePath = "/live/pulse"

// pulseSettle spaces one stream's re-lists, so an agent's crawl of hundreds of
// writes costs a swap a second, not one per write.
const pulseSettle = time.Second

var pulseRegions = sseRegions{"pulse": {"find .pulse-list", "outerHTML"}}

// pulseVM is a pane's recent activity and the stream, if any, that keeps it current.
type pulseVM struct {
	StreamURL   string // "" for a pane without focus or a room without a feed
	Items       []pulseItemVM
	Unavailable bool // no list to show; a stream's first list replaces the note
}

// pulseItemVM is one write: what happened to which document, and who wrote it.
type pulseItemVM struct {
	What   string // "published · edition 3", "appended · edition 4", "archived"
	Path   string
	Href   string // the document, continuing the trail; "" once archived
	World  string
	Person string // the person badge: the verified user, when stamped
	Agent  string // the agent badge: the writing software
	At     string // the clock time the room heard of it, UTC
	Stamp  string // the same instant for <time datetime>
}

// livePulseURL is the stream the focused pane idx connects to; the trail
// rides along so the list's links continue it.
func livePulseURL(t trail, idx int) string {
	query := url.Values{"trail": {trailRest(t)}, "idx": {strconv.Itoa(idx)}}
	return LivePulsePath + "?" + query.Encode()
}

// pulseItems words each write for the list; links open the document beside
// pane idx.
func pulseItems(pulses []domain.Pulse, t trail, idx int) []pulseItemVM {
	items := make([]pulseItemVM, 0, len(pulses))
	for _, p := range pulses {
		at := p.At.UTC()
		item := pulseItemVM{What: pulseWhat(p.Change), Path: p.Path, World: p.World, Person: p.User, Agent: p.Agent,
			At: at.Format("15:04 UTC"), Stamp: at.Format(time.RFC3339)}
		if p.Op != domain.OpArchive {
			item.Href = trailDocLink(domain.Ref{World: p.World, Path: p.Path}, t, idx)
		}
		items = append(items, item)
	}
	return items
}

func pulseWhat(change domain.Change) string {
	what := "written"
	switch change.Op {
	case domain.OpPublish:
		what = "published"
	case domain.OpAppend:
		what = "appended"
	case domain.OpArchive:
		return "archived"
	}
	if change.Version > 0 {
		what += " · edition " + strconv.Itoa(change.Version)
	}
	return what
}

func samePulse(a, b domain.Pulse) bool { return a.Change == b.Change && a.At.Equal(b.At) }

// showsPulse reports a pane that lists recent activity: the universe floor or
// the activity pane.
func showsPulse(p paneAddr) bool {
	return p == paneAddr{Kind: paneFloor} || p == paneAddr{Kind: paneActivity}
}

// LivePulse streams a pane's recent activity until the reader leaves.
func (h LiveHandler) LivePulse(c *echo.Context) error {
	if crossSiteStream(c) {
		return echo.NewHTTPError(http.StatusForbidden, "cross-site stream rejected")
	}
	if h.pulse == nil {
		return echo.NewHTTPError(http.StatusNotFound, "the pulse is off")
	}
	t, err := parseTrail(c.QueryParam("trail"), c.QueryParam("idx"), "")
	if err != nil || !showsPulse(t.Panes[t.Focus]) {
		return echo.NewHTTPError(http.StatusBadRequest, "the pulse streams to the universe floor or the activity pane")
	}
	ctx := c.Request().Context()
	// Subscribe before the first list, so a write between them still signals.
	changed := h.pulse.WatchPulse(ctx)
	out, err := openSSE(c, pulseRegions)
	if err != nil {
		return err
	}
	defer out.close()
	var shown []domain.Pulse
	// relist swaps the list in when it changed; false ends the stream: the
	// reader left, or the world list was refused.
	relist := func(first bool) bool {
		for {
			pulses, err := h.pulse.Pulse(ctx)
			switch {
			case ctx.Err() != nil:
				return false
			case errors.Is(err, domain.ErrUnauthorized):
				endRefused(out, first)
				return false
			case err != nil:
				// Transient: retry unprompted, since no later write may come.
				c.Logger().Warn("live pulse list failed", "err", err)
				if !pulseWait(ctx) {
					return false
				}
				continue
			case first || !slices.EqualFunc(pulses, shown, samePulse):
				// The first list always goes out, even empty: the render's may differ.
				shown = pulses
				out.send("pulse", fragment(c, "pulse-list", pulseItems(pulses, t, t.Focus)))
			}
			return true
		}
	}
	// The first list catches a write between the floor's render and now.
	if !relist(true) {
		return nil
	}
	for range changed {
		if !relist(false) || !pulseWait(ctx) {
			return nil
		}
	}
	return nil
}

// pulseWait waits out pulseSettle; writes meanwhile coalesce in the watcher's
// one pending signal. false means ctx ended.
func pulseWait(ctx context.Context) bool {
	timer := time.NewTimer(pulseSettle)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
