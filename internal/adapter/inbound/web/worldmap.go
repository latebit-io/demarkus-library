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
	"strconv"
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
// orbit spokes here. Documents are grouped into collapsible directory
// aggregates (worldmap_agg.go); an expanded group lays its members on a
// sunflower spiral ranked by degree, and unlinked documents draw dashed.
const (
	wmTierTop   = 44   // top margin above the spiral (clears the caption)
	wmPitch     = 58   // target distance (px) between neighbouring nodes
	wmHubNodes  = 13   // top-ranked nodes: edges among them form the rest-state spine
	wmLabelTop  = 13   // top-ranked nodes labeled at rest; the rest label on zoom/hover
	wmTierRatio = 1.85 // x:y stretch — the spiral is a wide ellipse so the layout
	// fills a wide overlay instead of letterboxing a near-square box.
	wmSideMargin = 80   // horizontal margin beyond the layout (room for labels)
	wmMinWidth   = 1100 // a small map still fills the overlay at a sane scale
	wmLabelTrim  = 18   // node label length cap (full title in <title>)
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
		opts := wmOpts{
			open: strings.Split(c.QueryParam("open"), ","),
			openURL: func(keys []string) string {
				return "/w/" + url.PathEscape(world) + "/u?overlay=1&open=" + url.QueryEscape(strings.Join(keys, ","))
			},
		}
		svg := worldMapRender(wm, func(p string) string {
			if len(t.Panes) > 0 {
				return trailURL(trailAfterClick(t, t.Focus, paneAddr{Kind: paneDoc, World: world, Value: p}))
			}
			return docRoute(world, p)
		}, worldNewURL(world, authed), opts)
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

// wmOpts carries the aggregation view state (plans/world-map-aggregation.md).
// open is the parsed `open` param; openURL builds the fragment URL for a new
// open set (nil for a non-interactive render, where aggregates link to their
// listing); chunk overrides wmChunk (tests).
type wmOpts struct {
	open    []string
	openURL func(keys []string) string
	chunk   int
}

// wmNodeStyle is a document node's rest-state treatment.
type wmNodeStyle struct {
	lod    bool // label only on zoom/hover
	orphan bool // no reference edge: dashed
}

// worldMapSVG renders the rest-state map with no open set (trail panes, tests).
func worldMapSVG(wm domain.WorldMap, docURL func(string) string, newURL string) template.HTML {
	return worldMapRender(wm, docURL, newURL, wmOpts{})
}

// worldMapRender draws the world's documents by reference connectivity (ADR
// 0006 §5) as collapsible directory groups: the visible items are laid out by
// footprint, document edges roll up to the items that represent their
// endpoints, and a caption tallies connected vs unlinked. docURL turns a
// document path into its navigation target; newURL, when non-empty, adds the
// "new document" affordance (the only entry point for an empty world, where
// there is no doc margin to host the usual "new" link).
func worldMapRender(wm domain.WorldMap, docURL func(string) string, newURL string, opts wmOpts) template.HTML {
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

	degree, rank, linked := wmRankDocs(docs, wm.Edges)

	chunk := opts.chunk
	if chunk <= 0 {
		chunk = wmChunk
	}
	open := wmParseOpen(opts.open)
	tree := wmBuildTree(docs)
	root, owner := wmVisible(tree, open, rank, degree, chunk)
	outerRy := int(wmMeasure(root))
	outerRx := int(float64(outerRy) * wmTierRatio)
	// The viewBox fits the content tightly — wide enough for the layout (plus
	// label margin) — so the SVG fills the wide overlay rather than centering a
	// square in it.
	width := max(2*outerRx+2*wmSideMargin, wmMinWidth)
	height := wmTierTop + 2*outerRy + 36
	wmPlace(root, width/2, wmTierTop+outerRy)
	items := wmFlatten(root, nil)

	rolled := wmRollup(wm.Edges, owner)
	vrank, spine := wmVisibleRank(items, rolled, rank)

	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="floor world-map" viewBox="0 0 %d %d" width="%d" height="%d" role="img" aria-label="%s map">`,
		width, height, width, height, html.EscapeString(wm.World.Name))
	caption := fmt.Sprintf("%d connected · %d unlinked", linked, len(docs)-linked)
	if shown := wmShownDocs(items); shown < len(docs) {
		caption += fmt.Sprintf(" · %d of %d shown", shown, len(docs))
	}
	fmt.Fprintf(&b, `<text class="world-map-caption" x="%d" y="22" text-anchor="middle">%s</text>`, width/2, caption)
	b.WriteString(arrowMarker)

	wmDrawEdges(&b, rolled, vrank, spine)
	// Labels at rest: the top-ranked documents, plus every root-level document
	// of a structured world (its landmarks, whatever their degree). A flat
	// world has no landmarks, only rank.
	landmarks := len(tree.subs) > 0
	wmDrawItems(&b, items, docURL, open, opts, func(it *wmItem) wmNodeStyle {
		rootDoc := landmarks && !strings.Contains(strings.TrimPrefix(it.doc.Path, "/"), "/")
		return wmNodeStyle{lod: vrank[it] >= wmLabelTop && !rootDoc && !it.ringed, orphan: degree[it.doc.Path] == 0}
	})
	b.WriteString(`</svg>`)
	if newURL != "" {
		b.WriteString(`<p class="world-map-new"><a href="` + html.EscapeString(newURL) + `" hx-boost="false" class="edit-link">+ new document</a></p>`)
	}
	return template.HTML(b.String()) //nolint:gosec // built from escaped parts; all node text/attrs pass html.EscapeString
}

// wmRankDocs ranks documents by degree (hubs first), then importance, then
// path, and counts the linked ones. Connectivity is by drawn edges, not the
// hub's orphan verdict: keying off d.Orphan silently flags nothing whenever
// the durable hub graph is sparse. Deterministic, so the layout is cacheable.
func wmRankDocs(docs []domain.FloorDoc, edges []domain.Edge) (degree, rank map[string]int, linked int) {
	degree = make(map[string]int, len(docs))
	for _, e := range edges {
		degree[e.From.Path]++
		degree[e.To.Path]++
	}
	ordered := make([]domain.FloorDoc, len(docs))
	copy(ordered, docs)
	sort.SliceStable(ordered, func(i, j int) bool {
		if di, dj := degree[ordered[i].Path], degree[ordered[j].Path]; di != dj {
			return di > dj
		}
		if ordered[i].Importance != ordered[j].Importance {
			return ordered[i].Importance > ordered[j].Importance
		}
		return ordered[i].Path < ordered[j].Path
	})
	rank = make(map[string]int, len(ordered))
	for i, d := range ordered {
		rank[d.Path] = i
		if degree[d.Path] > 0 {
			linked++
		}
	}
	return degree, rank, linked
}

// wmVisibleRank ranks the visible items by rolled-up degree (ties: document
// rank, aggregates first, then id) and returns the rank map plus the
// spanning-tree membership of the rolled edges. The top wmHubNodes form the
// spine and the top wmLabelTop documents are labeled at rest.
func wmVisibleRank(items []*wmItem, rolled []*wmRolled, rank map[string]int) (vrank map[*wmItem]int, tree []bool) {
	vdeg := map[*wmItem]int{}
	for _, e := range rolled {
		vdeg[e.from] += e.count
		vdeg[e.to] += e.count
	}
	second := func(it *wmItem) int {
		if it.kind == wmItemDoc {
			return rank[it.doc.Path]
		}
		return -1
	}
	byRank := append([]*wmItem(nil), items...)
	sort.SliceStable(byRank, func(i, j int) bool {
		if vdeg[byRank[i]] != vdeg[byRank[j]] {
			return vdeg[byRank[i]] > vdeg[byRank[j]]
		}
		if second(byRank[i]) != second(byRank[j]) {
			return second(byRank[i]) < second(byRank[j])
		}
		return byRank[i].id < byRank[j].id
	})
	vrank = make(map[*wmItem]int, len(byRank))
	for i, it := range byRank {
		vrank[it] = i
	}
	return vrank, wmSpanningTree(byRank, rolled)
}

// wmDrawEdges draws the rolled-up edges first (nodes draw on top). Rest-state
// tier: edges among the top-ranked items are the spine, spanning-tree spokes
// are faint, every other edge is near-invisible until its node is hovered. A
// bundle of several document edges draws thicker.
func wmDrawEdges(b *strings.Builder, rolled []*wmRolled, vrank map[*wmItem]int, tree []bool) {
	for i, e := range rolled {
		tier := "edge-dim"
		switch {
		case vrank[e.from] < wmHubNodes && vrank[e.to] < wmHubNodes:
			tier = "edge-spine"
		case tree[i]:
			tier = "edge-tree"
		}
		width := 0.0
		if e.count > 1 {
			tier += " edge-bundle"
			width = math.Min(4, 1.5+0.3*float64(e.count))
		}
		directedEdge(b, e.from.x, e.from.y, e.from.r, e.to.x, e.to.y, e.to.r, e.from.id, e.to.id, e.rel, tier, width)
	}
}

// wmDrawItems draws every visible item; style decides a document's label
// and orphan treatment.
func wmDrawItems(b *strings.Builder, items []*wmItem, docURL func(string) string, open wmOpenSet, opts wmOpts, style func(*wmItem) wmNodeStyle) {
	for _, it := range items {
		switch it.kind {
		case wmItemDoc:
			wmDocNode(b, it.doc, it.x, it.y, it.r, docURL, style(it))
		case wmItemGroup:
			wmAggNode(b, it, "+", it.group.key+" ("+strconv.Itoa(it.count)+")", docURL, open, opts, wmOpenExpand)
		case wmItemMore:
			wmAggNode(b, it, "…", strconv.Itoa(it.count)+" more", docURL, open, opts, wmOpenMore)
		case wmItemAnchor:
			for _, c := range it.children {
				if c.hub { // the hub holds the centre; the anchor sits just above it
					it.y = c.y - c.r - it.r - 6
					break
				}
			}
			wmAggNode(b, it, "−", it.group.key, docURL, open, opts, wmOpenCollapse)
		case wmItemRoot:
		}
	}
}

// wmShownDocs counts the documents drawn as their own node.
func wmShownDocs(items []*wmItem) int {
	n := 0
	for _, it := range items {
		if it.kind == wmItemDoc {
			n++
		}
	}
	return n
}

// wmDocNode draws one document node — a status-coded circle linking to the doc,
// labeled, with its full title in <title>.
func wmDocNode(b *strings.Builder, doc domain.FloorDoc, x, y, r int, docURL func(string) string, style wmNodeStyle) {
	cls := "floor-doc status-" + doc.Status
	label := "floor-doc-label"
	if style.orphan {
		cls += " world-map-orphan"
	}
	if style.lod {
		label += " label-lod"
	}
	fmt.Fprintf(b, `<a href="%s" data-node="%s"><circle class="%s" cx="%d" cy="%d" r="%d"/>`,
		html.EscapeString(docURL(doc.Path)), html.EscapeString(doc.Path), html.EscapeString(cls), x, y, r)
	fmt.Fprintf(b, `<text class="%s" x="%d" y="%d" text-anchor="middle">%s</text>`,
		label, x, y+r+13, html.EscapeString(trimRunes(doc.Title, wmLabelTrim)))
	fmt.Fprintf(b, `<title>%s — %s</title></a>`, html.EscapeString(doc.Title), html.EscapeString(doc.Path))
}

// wmAggNode draws a group, "more" or anchor node. Its href is the directory
// listing (the no-JS and trail-pane behaviour); with an openURL the click is
// an htmx swap of the map fragment with the group expanded, paged or
// collapsed.
func wmAggNode(b *strings.Builder, it *wmItem, glyph, label string, docURL func(string) string, open wmOpenSet, opts wmOpts, action wmOpenAction) {
	cls := "floor-agg"
	switch it.kind {
	case wmItemMore:
		cls += " floor-agg-more"
	case wmItemAnchor:
		cls += " floor-agg-anchor"
	case wmItemDoc, wmItemGroup, wmItemRoot:
	}
	fmt.Fprintf(b, `<a href="%s" data-node="%s"`, html.EscapeString(docURL(it.group.list)), html.EscapeString(it.id))
	if opts.openURL != nil {
		fmt.Fprintf(b, ` hx-get="%s" hx-target="#map-canvas" hx-swap="innerHTML"`,
			html.EscapeString(opts.openURL(open.with(it.group.key, action))))
	}
	fmt.Fprintf(b, `><circle class="%s" cx="%d" cy="%d" r="%d"/>`, cls, it.x, it.y, it.r)
	fmt.Fprintf(b, `<text class="floor-agg-glyph" x="%d" y="%d" text-anchor="middle">%s</text>`, it.x, it.y+4, glyph)
	// An anchor's label goes above it: below is the group's centre (a hub or
	// the first spiral member) and its label.
	ly := it.y + it.r + 13
	if it.kind == wmItemAnchor {
		ly = it.y - it.r - 5
	}
	fmt.Fprintf(b, `<text class="floor-doc-label" x="%d" y="%d" text-anchor="middle">%s</text>`,
		it.x, ly, html.EscapeString(trimRunes(label, wmLabelTrim+6)))
	fmt.Fprintf(b, `<title>%s — %d documents</title></a>`, html.EscapeString(it.group.list), it.count)
}

// wmSpanningTree picks the rest-state spokes: a BFS forest over the rolled-up
// edges, rooted at the highest-ranked visible item of each component (byRank
// is the degree-ranked item list). Neighbours are visited in rank order, so
// an item with several same-depth parents attaches to the best-connected one
// and the choice is deterministic. Returns tree membership indexed like edges.
func wmSpanningTree(byRank []*wmItem, edges []*wmRolled) []bool {
	rank := make(map[*wmItem]int, len(byRank))
	for i, it := range byRank {
		rank[it] = i
	}
	type arc struct{ to, edge int }
	adj := make([][]arc, len(byRank))
	for i, e := range edges {
		f, t := rank[e.from], rank[e.to]
		adj[f] = append(adj[f], arc{t, i})
		adj[t] = append(adj[t], arc{f, i})
	}
	for _, ns := range adj {
		sort.Slice(ns, func(i, j int) bool { return ns[i].to < ns[j].to })
	}
	tree := make([]bool, len(edges))
	seen := make([]bool, len(byRank))
	for root := range byRank {
		if seen[root] {
			continue
		}
		seen[root] = true
		queue := []int{root}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for _, a := range adj[cur] {
				if seen[a.to] {
					continue
				}
				seen[a.to] = true
				tree[a.edge] = true
				queue = append(queue, a.to)
			}
		}
	}
	return tree
}

// wmSpiralScale converts a spiral index to a radius: r = scale·√i gives a
// mean area per node of π·scale², matched to hexagonal packing at wmPitch
// (area √3/2·pitch²) so neighbours land about wmPitch apart.
var wmSpiralScale = wmPitch * math.Sqrt(math.Sqrt(3)/2/math.Pi)

// wmSpiralRadius is the vertical radius needed to hold n nodes plus one
// node's worth of rim.
func wmSpiralRadius(n int) int {
	return int(wmSpiralScale*math.Sqrt(float64(max(n, 1)))) + wmPitch/2
}

// spiralAt places index i on the Vogel spiral around (cx, cy), stretched
// wmTierRatio-wide — deterministic, so the layout is cacheable.
func spiralAt(cx, cy, i int) (x, y int) {
	const golden = 2.399963229728653 // 137.5° in radians
	r := wmSpiralScale * math.Sqrt(float64(i))
	a := float64(i) * golden
	return cx + int(r*wmTierRatio*math.Cos(a)), cy + int(r*math.Sin(a))
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
