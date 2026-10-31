package web

// The live graph (plans/watch-realtime.md): while a graph overlay is open, its
// stream names each write the reader may see, and islands.js pulses or fades
// the matching node in place, keeping the reader's zoom, pan and exploration.

import (
	"net/http"
	"net/url"
	"slices"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// LiveGraphPath is the open graph overlays' write stream; LongLived.
const LiveGraphPath = "/live/graph"

// LiveGraph names, one event each, the writes that join the reader's recent
// activity after the stream connects. The list at connect is the baseline:
// writes before the overlay opened are already drawn.
func (h LiveHandler) LiveGraph(c *echo.Context) error {
	if crossSiteStream(c) {
		return echo.NewHTTPError(http.StatusForbidden, "cross-site stream rejected")
	}
	if h.pulse == nil {
		return echo.NewHTTPError(http.StatusNotFound, "the live graph is off")
	}
	var seen []domain.Pulse
	return h.followPulse(c, nil, func(out *sseWriter, pulses []domain.Pulse, first bool) {
		if !first {
			// Oldest first, the order they were written in.
			for _, p := range slices.Backward(pulses) {
				if !slices.ContainsFunc(seen, func(s domain.Pulse) bool { return samePulse(s, p) }) {
					out.send("write", graphWrite(p))
				}
			}
		}
		seen = pulses
	})
}

// graphWrite is one write as the stream names it, query-encoded: the op, the
// world (keyed, as the graphs' data-world is) and the document, and new for a
// first edition, which no open graph has a node for yet.
func graphWrite(p domain.Pulse) string {
	query := url.Values{"op": {p.Op}, "world": {domain.WorldKey(p.World)}, "path": {p.Path}}
	if p.Op == domain.OpPublish && p.Version == 1 {
		query.Set("new", "1")
	}
	return query.Encode()
}

// liveGraphURL is the stream an open graph overlay connects to; "" without a
// change feed, so the page never retries a stream that would 404.
func (h *ReadingHandler) liveGraphURL() string {
	if !h.liveEditions {
		return ""
	}
	return LiveGraphPath
}
