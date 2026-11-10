package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

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

// paletteTextRows caps the section-text matches under the name matches.
const paletteTextRows = 8

// paletteTextMin is the shortest query searched in section text: shorter
// ones match too much to be worth a catalog round trip per keystroke.
const paletteTextMin = 3

// scopeUniverse is the scope parameter's value for every authorized world.
const scopeUniverse = "universe"

// nameIndexer is the catalog's name index, cached: the palette matches it
// on every keystroke.
type nameIndexer interface {
	NameIndexCached(ctx context.Context, world string) ([]domain.IndexEntry, error)
}

// textSearcher is the catalog's section-text search.
type textSearcher interface {
	Lookup(ctx context.Context, q domain.CatalogQuery) (domain.CatalogResult, error)
}

// PaletteHandler serves the command palette's htmx fragments (ADR 0006 §3):
// name matches, and the section-text matches loaded under them.
type PaletteHandler struct {
	index        nameIndexer
	text         textSearcher
	defaultWorld string
	terms        Terms
}

type paletteRow struct {
	Title   string
	Loc     string // world + path, the mono secondary line
	Status  string
	Snippet string // the matching excerpt, on a section-text row
	URL     string // a ready trail URL (rewind-on-dedup, else push)
}

type paletteVM struct {
	Query   string
	Rows    []paletteRow
	Warning string
	TextURL string // the section-text fragment to load under the rows, "" for none
}

// Palette renders the name matches for the htmx active search. Without htmx
// (no JS, or a direct hit) it redirects to /search, the durable surface.
func (h *PaletteHandler) Palette(c *echo.Context) error {
	if c.Request().Header.Get("HX-Request") != "true" {
		target := "/search"
		if q := strings.TrimSpace(c.QueryParam("q")); q != "" {
			target += "?q=" + url.QueryEscape(q)
		}
		return c.Redirect(http.StatusSeeOther, target)
	}

	t := currentTrail(c)
	q := strings.TrimSpace(c.QueryParam("q"))
	if q == "" {
		// "Get back to where I was" is the common retrieval.
		return c.Render(http.StatusOK, "palette-results", paletteVM{Rows: recentRows(t, h.terms)})
	}
	world := h.searchWorld(c, t)
	entries, err := h.index.NameIndexCached(c.Request().Context(), world)
	partial, isPartial := errors.AsType[*domain.PartialLookupError](err)
	if err != nil && !isPartial {
		// Surface real failures (re-login, unreachable world) instead of
		// rendering an outage as "no matches".
		return h.fail(c, err, world, "/palette")
	}
	vm := paletteVM{Query: q, Rows: matchRows(t, q, entries)}
	if isPartial {
		vm.Warning = partial.Error()
	}
	if h.searchesText(q) {
		params := url.Values{"q": {q}, "world": {world}}
		if world == "" {
			params = url.Values{"q": {q}, "scope": {scopeUniverse}}
		}
		vm.TextURL = "/palette/text?" + params.Encode()
	}
	return c.Render(http.StatusOK, "palette-results", vm)
}

// Text renders the section-text matches for q: the rows the name index
// cannot give, loaded under it so a slow search never holds the names back.
func (h *PaletteHandler) Text(c *echo.Context) error {
	q := strings.TrimSpace(c.QueryParam("q"))
	if !h.searchesText(q) || !wantsFragment(c.Request()) {
		return c.NoContent(http.StatusNoContent)
	}
	t := currentTrail(c)
	world := h.searchWorld(c, t)
	result, err := h.text.Lookup(c.Request().Context(),
		domain.CatalogQuery{World: world, Query: q, Match: domain.MatchBody, Limit: paletteTextRows})
	if _, partial := errors.AsType[*domain.PartialLookupError](err); err != nil && !partial {
		return h.fail(c, err, world, "/palette/text")
	}
	// A world without section search answers from the catalog: rows the
	// name matches already show.
	if !result.BodyMatched {
		return c.NoContent(http.StatusNoContent)
	}
	return c.Render(http.StatusOK, "palette-text", paletteVM{Rows: textRows(t, result.Hits)})
}

// searchesText reports a query worth a section-text search, where there is one.
func (h *PaletteHandler) searchesText(q string) bool {
	return h.text != nil && utf8.RuneCountInString(q) >= paletteTextMin
}

// searchWorld is the world searched: "" for every world when the reader
// asked for the universe, else the world the reader is in.
func (h *PaletteHandler) searchWorld(c *echo.Context, t trail) string {
	if c.QueryParam("scope") == scopeUniverse {
		return ""
	}
	if world := c.QueryParam("world"); world != "" {
		return world
	}
	return paletteWorld(c, t, h.defaultWorld)
}

// fail presents a read failure of world, or of the universe when world is "".
func (h *PaletteHandler) fail(c *echo.Context, err error, world, path string) error {
	if world == "" {
		return presentRoomError(c, err, h.terms.UniverseLower())
	}
	return presentError(c, err, world, path)
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
func recentRows(t trail, terms Terms) []paletteRow {
	var rows []paletteRow
	for i, v := range slices.Backward(t.Panes) {
		if i == t.Focus {
			continue
		}
		title, loc := paneLabel(v, terms)
		rows = append(rows, paletteRow{Title: title, Loc: loc, URL: trailURL(trailFocused(t, i))})
	}
	return rows
}

// matchRows ranks the name index against q, a row per hit.
func matchRows(t trail, q string, entries []domain.IndexEntry) []paletteRow {
	hits := domain.RankIndex(q, entries, paletteMaxRows)
	rows := make([]paletteRow, 0, len(hits))
	for _, e := range hits {
		row := docRow(t, domain.Ref{World: e.World, Path: e.Path}, e.Title)
		row.Status = e.Status
		rows = append(rows, row)
	}
	return rows
}

// textRows builds a row per section-text hit, in the catalog's rank order,
// linked to the section: a heading's id is its anchor.
func textRows(t trail, hits []domain.CatalogHit) []paletteRow {
	rows := make([]paletteRow, 0, len(hits))
	for i := range hits {
		hit := &hits[i]
		row := docRow(t, hit.Ref, hit.Title)
		row.Status, row.Snippet = hit.Status, hit.Snippet
		if hit.Anchor != "" {
			row.URL += "#" + hit.Anchor
		}
		rows = append(rows, row)
	}
	return rows
}

// docRow is a row jumping to a document: a push onto the end of the trail,
// or a rewind when the document is already on it. An empty trail starts one.
func docRow(t trail, doc domain.Ref, title string) paletteRow {
	return paletteRow{Title: title, Loc: doc.World + doc.Path, URL: trailDocLink(doc, t, len(t.Panes)-1)}
}

// paneLabel gives a recent row its title + location from a pane address.
func paneLabel(p paneAddr, terms Terms) (title, loc string) {
	return paneKinds[p.Kind].label(p, terms)
}
