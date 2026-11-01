package web

// The live graph (plans/watch-realtime.md): while a graph overlay is open, its
// stream names each write the reader may see, and islands.js pulses or fades
// the matching node in place, keeping the reader's zoom, pan and exploration.

import (
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// LiveGraphPath is the open graph overlays' write stream; LongLived.
const LiveGraphPath = "/live/graph"

// LiveGraph names, one event each, the writes in the reader's recent activity
// after graphSince, so a write between the drawing and the stream still lands.
// Each frame's id is its write time, the cursor a reconnect resumes from.
func (h LiveHandler) LiveGraph(c *echo.Context) error {
	if crossSiteStream(c) {
		return echo.NewHTTPError(http.StatusForbidden, "cross-site stream rejected")
	}
	if h.pulse == nil {
		return echo.NewHTTPError(http.StatusNotFound, "the live graph is off")
	}
	since := graphSince(c.Request())
	return h.followPulse(c, nil, func(out *sseWriter, pulses []domain.Pulse, _ bool) {
		// Oldest first, the order they were written in.
		for _, p := range slices.Backward(pulses) {
			if at := p.At.UnixMicro(); at > since {
				out.sendID("write", strconv.FormatInt(at, 10), graphWrite(p))
				since = at
			}
		}
	})
}

// graphSince is the write time, in microseconds, a stream resumes after: the
// last frame the client saw (Last-Event-ID, on EventSource's own reconnect),
// else the drawing the overlay shows (since, its data-drawn), else now.
func graphSince(r *http.Request) int64 {
	for _, mark := range []string{r.Header.Get("Last-Event-ID"), r.URL.Query().Get("since")} {
		if at, err := strconv.ParseInt(mark, 10, 64); err == nil {
			return at
		}
	}
	return time.Now().UnixMicro()
}

// drawnAt stamps a drawing's root element with when its read began, the
// since its overlay's stream connects with. Every drawing opens with its root
// tag, and escapes '>' in its attribute values.
func drawnAt(drawing template.HTML, read time.Time) template.HTML {
	s := string(drawing)
	end := strings.IndexByte(s, '>')
	if end < 0 {
		return drawing
	}
	return template.HTML(s[:end] + ` data-drawn="` + strconv.FormatInt(read.UnixMicro(), 10) + `"` + s[end:]) //nolint:gosec // an integer attribute on our own markup
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
