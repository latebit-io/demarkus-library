package web

import (
	"context"
	"fmt"
	"html"
	"html/template"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// The world-view zoom level (ADR 0005 decision 4; plans §"World-view zoom
// level"). The floor one zoom in: a single world's catalog grouped into
// top-level directory clusters, each a labeled hub with its top-importance
// documents orbiting and a "+N more" aggregate that opens the directory's
// listing pane. Server-rendered SVG like the floor and the graph pane — same
// deterministic layout, zero new JS, ADR 0003's canvas island stays unspent.
// Every node is a plain <a> whose href continues the trail.
// Reference-only layout (ADR 0006 §5): the map draws references, not
// containment — directories are the index's job, so there are no dir hubs or
// orbit spokes here. Reference-linked documents sit in a connected ring; orphans
// (zero reference edges, per the durable hub graph) float apart in a band below.
const (
	wmTierTop   = 44   // top margin above the outermost ring (clears the caption)
	wmTierBaseR = 90   // innermost (hub) ring vertical radius (ry)
	wmTierGap   = 86   // ry step between concentric rings
	wmTierMaxR  = 300  // outermost ry cap (the radial tail soaks into this ring)
	wmTierArc   = 58   // min arc (px) between adjacent nodes on a ring → capacity
	wmTierRatio = 1.85 // x:y stretch — rings are wide ellipses so the radial layout
	// fills a wide overlay instead of letterboxing a near-square box.
	wmSideMargin = 80 // horizontal margin beyond the widest ring (room for labels)
	wmLabelTrim  = 18 // ring node label length cap (full title in <title>)
	// The unlinked band is a secondary signal — kept compact so the connected
	// graph dominates: shallow rows, small nodes, harder-trimmed labels. Its
	// column count is derived from the canvas width (full-width strip).
	wmBandLabelTrim = 10 // band node label length cap (tighter than the ring)
	wmOrphanCellW   = 70
	wmOrphanCellH   = 44
)

// WorldMapPage renders a world's map as a standalone permalink — /w/:world/u —
// the chunk-tail source and projection escape (ADR 0005 decision 12). On the
// canvas the same map renders as a trail pane.
func (h *ReadingHandler) WorldMapPage(c *echo.Context) error {
	world := c.Param("world")
	// A plain navigation to the standalone map lands on the canvas (with the map
	// pane); the overlay pull-up (?overlay=1) is always served as a fragment.
	if c.QueryParam("overlay") != "1" {
		if u := canvasTrailURL(c, paneAddr{Kind: paneFloor, World: world}); u != "" {
			return c.Redirect(http.StatusSeeOther, u)
		}
	}
	wm, err := h.reading.WorldMap(c.Request().Context(), world)
	if err != nil {
		return presentError(c, err, world, "/")
	}
	authed := c.Get(authedKey) != nil

	// Overlay mode (ADR 0006 §5): the on-demand map pull-up htmx-loads this with
	// ?overlay=1. Its nodes extend the reader's trail (from HX-Current-URL), and
	// it returns a bare SVG fragment to swap into the overlay.
	if c.QueryParam("overlay") == "1" {
		t := currentTrail(c)
		svg := worldMapSVG(wm, func(p string) string {
			if len(t.Panes) > 0 {
				return trailURL(trailAfterClick(t, t.Focus, paneAddr{Kind: paneDoc, World: world, Value: p}))
			}
			return docRoute(world, p)
		}, worldNewURL(world, authed))
		return c.HTML(http.StatusOK, string(svg))
	}

	// Single-pane permalink: nodes link to /w/ permalinks.
	svg := worldMapSVG(wm,
		func(p string) string { return docRoute(world, p) },
		worldNewURL(world, authed))
	vm := page{
		Title:         "Map: " + world,
		Host:          world,
		Path:          "/",
		Content:       svg,
		World:         world,
		WorldPath:     url.PathEscape(world),
		Authenticated: c.Get(authedKey) != nil,
		User:          userEmail(c),
	}
	return c.Render(http.StatusOK, h.templateFor(c), vm)
}

// worldMapPaneView builds the world-map pane on the trail canvas (focused-live
// like every pane): nodes link to post-click trail URLs so a click continues
// the trail (decision 4). Like the floor, the map carries no margin — its
// signals are on the nodes (status strokes, importance sizing).
// worldNewURL is the world-map's "new document" affordance target — create at
// the world root — or "" for an unauthenticated reader (writes are gated on a
// session, same posture as the doc-margin "new").
func worldNewURL(world string, authed bool) string {
	if !authed {
		return ""
	}
	return "/w/" + url.PathEscape(world) + "/new?dir=" + url.QueryEscape("/")
}

func (h *ReadingHandler) worldMapPaneView(ctx context.Context, t trail, i int, addr paneAddr, authed bool) (paneVM, error) {
	focused := i == t.Focus
	var wm domain.WorldMap
	var err error
	if focused {
		wm, err = h.reading.WorldMap(ctx, addr.World)
	} else {
		wm, err = h.reading.WorldMapCached(ctx, addr.World)
	}
	if err != nil {
		return paneVM{}, err
	}

	mode := "spine"
	switch {
	case focused:
		mode = "focused"
	case i == t.Focus-1:
		mode = "body"
	}
	vm := paneVM{
		Mode:     mode,
		Kind:     paneFloor,
		FocusURL: trailURL(trailFocused(t, i)),
		Title:    "Map: " + addr.World,
		World:    addr.World,
	}
	if mode == "spine" {
		return vm, nil
	}
	vm.Content = worldMapSVG(wm,
		func(p string) string {
			return trailURL(trailAfterClick(t, i, paneAddr{Kind: paneDoc, World: addr.World, Value: p}))
		},
		worldNewURL(addr.World, authed))
	return vm, nil
}

// wmPlaced is a rendered document node with its computed position.
type wmPlaced struct {
	doc  domain.FloorDoc
	x, y int
	r    int
}

// worldMapSVG draws the world's documents by reference connectivity (ADR 0006
// §5): documents with ≥1 drawn edge in a connected ring with their reference
// edges, the rest floated apart in a band below — the one signal the index
// can't give. A caption tallies connected vs unlinked. docURL turns a document
// path into its navigation target; newURL, when non-empty, adds the "new
// document" affordance (the only entry point for an empty world, where there is
// no doc margin to host the usual "new" link).
func worldMapSVG(wm domain.WorldMap, docURL func(string) string, newURL string) template.HTML {
	n := 0
	for _, cl := range wm.Clusters {
		n += len(cl.Docs)
	}
	docs := make([]domain.FloorDoc, 0, n)
	for _, cl := range wm.Clusters {
		docs = append(docs, cl.Docs...)
	}
	if len(docs) == 0 {
		// Unreadable ≠ empty: a read failure shows a notice and no create link
		// (we don't know the catalog is empty); a genuinely empty world offers
		// to create its first document (the only entry point for that case).
		if wm.Unreadable {
			return template.HTML(`<p class="floor-empty">This world's catalog could not be read.</p>`) //nolint:gosec // static markup
		}
		msg := `<p class="floor-empty">This world's catalog is empty.`
		if newURL != "" {
			msg += ` <a href="` + html.EscapeString(newURL) + `" hx-boost="false">Create the first document.</a>`
		}
		msg += `</p>`
		return template.HTML(msg) //nolint:gosec // newURL is server-constructed (/w/<escaped world>/new), text is static
	}

	// Ring membership is by drawn connectivity, not the hub's orphan verdict: a
	// node joins the connected ring iff it has ≥1 edge in the set we actually
	// draw (wm.Edges = hub ∪ observed, already filtered to labeled nodes).
	// Keying off d.Orphan instead silently collapses to "everything" whenever
	// the durable hub graph is sparse — worldOrphans then flags nothing, so the
	// whole catalog defaults onto one ring (a 30+ node hairball over a handful
	// of edges) while the band sits empty. Connectivity is the signal the index
	// can't give and the one the ring exists to show.
	connected := make(map[string]bool, len(wm.Edges)*2)
	for _, e := range wm.Edges {
		connected[e.From.Path] = true
		connected[e.To.Path] = true
	}
	var linked, loose []domain.FloorDoc
	for _, d := range docs {
		if connected[d.Path] {
			linked = append(linked, d)
		} else {
			loose = append(loose, d)
		}
	}

	// Degree-tiered concentric rings. A node's degree (its edge count in
	// wm.Edges) sets its tier: the most-connected documents fill the innermost
	// ring, the long low-degree tail fans out across larger rings. This pulls
	// the structural hubs to the centre so their many spokes radiate outward
	// instead of chording across one crowded ring, and keeps each ring sparse
	// enough to read (capacity grows with circumference). Deterministic — ties
	// break by importance then path — so the layout stays cacheable.
	degree := make(map[string]int, len(linked))
	for _, e := range wm.Edges {
		degree[e.From.Path]++
		degree[e.To.Path]++
	}
	ordered := make([]domain.FloorDoc, len(linked))
	copy(ordered, linked)
	sort.SliceStable(ordered, func(i, j int) bool {
		if di, dj := degree[ordered[i].Path], degree[ordered[j].Path]; di != dj {
			return di > dj
		}
		if ordered[i].Importance != ordered[j].Importance {
			return ordered[i].Importance > ordered[j].Importance
		}
		return ordered[i].Path < ordered[j].Path
	})

	// First pass: bucket nodes into concentric rings, inner (hub) ring first,
	// each ring holding ⌊circumference / wmTierArc⌋ nodes. Once a ring hits the
	// radius cap, it absorbs the whole remaining tail (the degenerate
	// large-catalog case) rather than spawning overlapping max-radius rings.
	type ringBucket struct {
		r    int
		docs []domain.FloorDoc
	}
	var rings []ringBucket
	for idx, k := 0, 0; idx < len(ordered); k++ {
		ry := min(wmTierBaseR+k*wmTierGap, wmTierMaxR)
		rx := int(float64(ry) * wmTierRatio)
		// Capacity ≈ ellipse perimeter / min arc (π(rx+ry) approximates it); the
		// wide ring holds more, so fewer rings spread across the width.
		n := max(1, int(math.Pi*float64(rx+ry))/wmTierArc)
		if ry == wmTierMaxR {
			n = len(ordered) - idx // capped ring soaks up the rest
		}
		n = min(n, len(ordered)-idx)
		rings = append(rings, ringBucket{r: ry, docs: ordered[idx : idx+n]})
		idx += n
	}

	outerRy := wmTierBaseR
	if len(rings) > 0 {
		outerRy = rings[len(rings)-1].r
	}
	outerRx := int(float64(outerRy) * wmTierRatio)
	// The viewBox fits the content tightly — wide enough for the widest ring (plus
	// label margin), and the unlinked band runs the full width — so the SVG fills
	// the wide overlay rather than centering a square in it.
	width := 2*outerRx + 2*wmSideMargin
	bandCols := max(1, width/wmOrphanCellW)
	cx, cy := width/2, wmTierTop+outerRy
	placed := make(map[string]wmPlaced, len(ordered))
	for _, rb := range rings {
		rx := int(float64(rb.r) * wmTierRatio)
		for j, d := range rb.docs {
			x, y := ellipseAt(cx, cy, j, len(rb.docs), rx, rb.r)
			placed[d.Path] = wmPlaced{doc: d, x: x, y: y, r: 5 + int(d.Importance*9)}
		}
	}
	height := cy + outerRy + 36

	// Unlinked band below, floated apart (no edges) — visually separate.
	orphanTop := height + 20
	if len(loose) > 0 {
		rows := (len(loose) + bandCols - 1) / bandCols
		height = orphanTop + rows*wmOrphanCellH + 12
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="floor world-map" viewBox="0 0 %d %d" width="%d" height="%d" role="img" aria-label="%s map">`,
		width, height, width, height, html.EscapeString(wm.World.Name))
	fmt.Fprintf(&b, `<text class="world-map-caption" x="%d" y="22" text-anchor="middle">%d connected · %d unlinked</text>`,
		width/2, len(linked), len(loose))
	b.WriteString(arrowMarker)

	// Reference edges first (nodes draw on top), only between placed (linked)
	// nodes, drawn From→To as arrows trimmed to each node's rim.
	for _, e := range wm.Edges {
		from, okF := placed[e.From.Path]
		to, okT := placed[e.To.Path]
		if !okF || !okT {
			continue
		}
		directedEdge(&b, from.x, from.y, from.r, to.x, to.y, to.r, e.From.Path, e.To.Path)
	}
	for _, d := range linked {
		pn := placed[d.Path]
		wmDocNode(&b, d, pn.x, pn.y, pn.r, docURL, false)
	}

	if len(loose) > 0 {
		fmt.Fprintf(&b, `<text class="world-map-band" x="20" y="%d">unlinked</text>`, orphanTop-10)
		for j, d := range loose {
			x := (j%bandCols)*wmOrphanCellW + wmOrphanCellW/2
			y := orphanTop + (j/bandCols)*wmOrphanCellH + wmOrphanCellH/2
			wmDocNode(&b, d, x, y, 5, docURL, true)
		}
	}
	b.WriteString(`</svg>`)
	if newURL != "" {
		b.WriteString(`<p class="world-map-new"><a href="` + html.EscapeString(newURL) + `" hx-boost="false" class="edit-link">+ new document</a></p>`)
	}
	return template.HTML(b.String()) //nolint:gosec // built from escaped parts; all node text/attrs pass html.EscapeString
}

// wmDocNode draws one document node — a status-coded circle linking to the doc,
// labeled, with its full title in <title>. orphan adds the orphan class so the
// floated band reads differently from the connected cluster.
func wmDocNode(b *strings.Builder, doc domain.FloorDoc, x, y, r int, docURL func(string) string, orphan bool) {
	cls := "floor-doc status-" + doc.Status
	trim := wmLabelTrim
	if orphan {
		cls += " world-map-orphan"
		trim = wmBandLabelTrim // band labels are tighter, to stay compact
	}
	fmt.Fprintf(b, `<a href="%s" data-node="%s"><circle class="%s" cx="%d" cy="%d" r="%d"/>`,
		html.EscapeString(docURL(doc.Path)), html.EscapeString(doc.Path), html.EscapeString(cls), x, y, r)
	fmt.Fprintf(b, `<text class="floor-doc-label" x="%d" y="%d" text-anchor="middle">%s</text>`,
		x, y+r+13, html.EscapeString(trimRunes(doc.Title, trim)))
	fmt.Fprintf(b, `<title>%s — %s</title></a>`, html.EscapeString(doc.Title), html.EscapeString(doc.Path))
}

// ellipseAt places slot j of n on an ellipse of radii (rx, ry) around (cx, cy),
// starting at the top and going clockwise — deterministic, so the layout is
// cacheable. rx > ry makes the rings wide, spreading nodes across the canvas so
// the radial layout fills a wide overlay rather than letterboxing a square.
func ellipseAt(cx, cy, j, n, rx, ry int) (x, y int) {
	angle := 2*math.Pi*float64(j)/float64(n) - math.Pi/2
	return cx + int(float64(rx)*math.Cos(angle)), cy + int(float64(ry)*math.Sin(angle))
}

// floorSpineTitle names a floor-kind tombstone/spine pane: "Universe" for the
// bare floor, "Map: <world>" for a world map.
func floorSpineTitle(addr paneAddr) string {
	if addr.World == "" {
		return "Universe"
	}
	return "Map: " + addr.World
}

// trimRunes caps a label to n runes, eliding with an ellipsis; the full title
// always rides in the node's <title>.
func trimRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "…"
}
