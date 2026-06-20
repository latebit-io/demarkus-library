package web

import (
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// The command palette (ADR 0006 §3) — the known-item "front door". SSR/htmx-hard
// (ADR 0003): it is an htmx active-search surface, never JSON and never a client
// state island. The overlay markup and every result row are server-rendered;
// typing fires an hx-get that swaps in an HTML fragment, and the whole thing
// degrades to the /search route without JS. The current trail rides in on the
// htmx HX-Current-URL header, so both the recent list (empty query) and the
// trail-extending row links are computed server-side through the trail codec —
// the URL stays the single source of truth, with no client-side trail logic.

// paletteMaxRows caps the rendered result list — a switcher shows the best
// matches, not the whole catalog.
const paletteMaxRows = 50

type paletteRow struct {
	Title  string
	Loc    string // world + path, the mono secondary line
	Status string
	URL    string // a ready trail URL (rewind-on-dedup, else push)
}

type paletteVM struct {
	Query string
	Rows  []paletteRow
}

// Palette renders the name-mode results fragment for the htmx active search. A
// non-htmx request (no JS, or a direct hit) is redirected to /search: the
// palette is a progressive enhancement, and /search is the durable, fully
// server-rendered surface it degrades to.
func (h *ReadingHandler) Palette(c *echo.Context) error {
	if c.Request().Header.Get("HX-Request") != "true" {
		target := "/search"
		if q := strings.TrimSpace(c.QueryParam("q")); q != "" {
			target += "?q=" + url.QueryEscape(q)
		}
		return c.Redirect(http.StatusSeeOther, target)
	}

	t := currentTrail(c)
	q := strings.TrimSpace(c.QueryParam("q"))

	var rows []paletteRow
	if q == "" {
		rows = recentRows(t) // "get back to where I was" is the common retrieval
	} else {
		world := c.QueryParam("world")
		if world == "" {
			world = paletteWorld(c, t, h.defaultWorld)
		}
		entries, err := h.reading.NameIndex(c.Request().Context(), c.QueryParam("scope"), world)
		if err != nil {
			// Surface real failures (re-login, unreachable world) instead of
			// rendering an outage as "no matches".
			return presentError(c, err, world, "/palette")
		}
		rows = matchRows(t, q, entries)
	}
	return c.Render(http.StatusOK, "palette-results", paletteVM{Query: q, Rows: rows})
}

// currentTrail parses the reader's current trail from the htmx HX-Current-URL
// header. Off a /t/ page (the floor, a permalink) or on any parse failure it is
// the empty trail (Focus -1) — recent is then empty and a jump starts fresh.
func currentTrail(c *echo.Context) trail {
	cur := c.Request().Header.Get("HX-Current-URL")
	if cur == "" {
		return trail{Focus: -1}
	}
	u, err := url.Parse(cur)
	if err != nil {
		return trail{Focus: -1}
	}
	rest, ok := strings.CutPrefix(u.Path, "/t/")
	if !ok {
		return trail{Focus: -1}
	}
	t, err := parseTrail(rest, u.Query().Get("focus"), u.Query().Get("reader"))
	if err != nil {
		return trail{Focus: -1}
	}
	return t
}

// paletteWorld is the world the search scopes to by default: the focused pane's
// world on a trail, the /w/<world>/ world on a permalink page, else the default
// (the floor and other world-less pages).
func paletteWorld(c *echo.Context, t trail, fallback string) string {
	if t.Focus >= 0 && t.Focus < len(t.Panes) && t.Panes[t.Focus].World != "" {
		return t.Panes[t.Focus].World
	}
	if w := worldFromURL(c.Request().Header.Get("HX-Current-URL")); w != "" {
		return w
	}
	return fallback
}

// worldFromURL pulls the world out of a /w/<world>/... current-URL path,
// returning "" when the path is not a /w/ permalink.
func worldFromURL(cur string) string {
	if cur == "" {
		return ""
	}
	u, err := url.Parse(cur)
	if err != nil {
		return ""
	}
	rest, ok := strings.CutPrefix(u.Path, "/w/")
	if !ok {
		return ""
	}
	w, _, _ := strings.Cut(rest, "/")
	if dec, derr := url.PathUnescape(w); derr == nil {
		w = dec
	}
	return w
}

// recentRows is the empty-query view: the trail in reverse (most-recent first),
// the active pane skipped. Each row rewinds (focuses) its pane.
func recentRows(t trail) []paletteRow {
	var rows []paletteRow
	for i := len(t.Panes) - 1; i >= 0; i-- {
		if i == t.Focus {
			continue
		}
		title, loc := paneLabel(t.Panes[i])
		rows = append(rows, paletteRow{Title: title, Loc: loc, URL: trailURL(trailFocused(t, i))})
	}
	return rows
}

// matchRows fuzzy-filters the catalog and builds a trail-extending link per hit.
// A jump pushes onto the end of the trail (trailAfterClick rewinds instead if
// the doc is already on it).
func matchRows(t trail, q string, entries []domain.IndexEntry) []paletteRow {
	idx := len(t.Panes) - 1 // -1 on an empty trail ⇒ a jump starts a fresh one
	ql := strings.ToLower(q)
	type hit struct {
		e    domain.IndexEntry
		rank int
	}
	var hits []hit
	for _, e := range entries {
		if r, ok := matchRank(ql, strings.ToLower(e.Title+" "+e.World+e.Path)); ok {
			hits = append(hits, hit{e, r})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].rank < hits[j].rank })
	if len(hits) > paletteMaxRows {
		hits = hits[:paletteMaxRows]
	}
	rows := make([]paletteRow, 0, len(hits))
	for _, h := range hits {
		target := paneAddr{Kind: paneDoc, World: h.e.World, Value: h.e.Path}
		rows = append(rows, paletteRow{
			Title:  h.e.Title,
			Loc:    h.e.World + h.e.Path,
			Status: h.e.Status,
			URL:    trailURL(trailAfterClick(t, idx, target)),
		})
	}
	return rows
}

// matchRank scores a match: a substring hit ranks by its position (earlier is
// better); a looser subsequence hit ranks behind every substring hit. Not a
// match ⇒ ok=false.
func matchRank(q, hay string) (int, bool) {
	if i := strings.Index(hay, q); i >= 0 {
		return i, true
	}
	j := 0
	for i := 0; i < len(hay) && j < len(q); i++ {
		if hay[i] == q[j] {
			j++
		}
	}
	if j == len(q) {
		return 1000, true
	}
	return 0, false
}

// paneLabel gives a recent row its title + location from a pane address.
func paneLabel(p paneAddr) (title, loc string) {
	switch p.Kind {
	case paneFloor:
		if p.World == "" {
			return "universe", ""
		}
		return p.World + " — map", p.World
	case paneTag:
		return "#" + p.Value, p.World
	default: // paneDoc, paneGraph
		name := strings.TrimSuffix(p.Value[strings.LastIndex(p.Value, "/")+1:], ".md")
		if name == "" {
			name = p.Value
		}
		if p.Kind == paneGraph {
			name = "graph: " + name
		}
		return name, p.World + p.Value
	}
}
