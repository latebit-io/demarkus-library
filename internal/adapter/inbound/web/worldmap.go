package web

import (
	"fmt"
	"html"
	"html/template"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"

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

// worldNewURL is the world-map's "new document" affordance target — create at
// the world root — or "" for an unauthenticated reader (writes are gated on a
// session, same posture as the doc-margin "new").
func worldNewURL(world string, authed bool) string {
	if !authed {
		return ""
	}
	return "/w/" + url.PathEscape(world) + "/new?dir=" + url.QueryEscape("/")
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

// wmDraw is the context every aggregate node shares: how a path becomes a URL,
// the open set a click transitions from, and the view options that decide
// whether aggregates are htmx-clickable at all.
type wmDraw struct {
	docURL func(string) string
	open   wmOpenSet
	opts   wmOpts
}

// wmAgg is what distinguishes one aggregate node from another: the glyph in
// its circle, its label, and the open-set transition a click performs.
type wmAgg struct {
	glyph  string
	label  string
	action wmOpenAction
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

	ranking, linked := wmRankDocs(docs, wm.Edges)

	chunk := opts.chunk
	if chunk <= 0 {
		chunk = wmChunk
	}
	open := wmParseOpen(opts.open)
	tree := wmBuildTree(docs)
	root, owner := wmVisible(tree, open, ranking, chunk)
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
	vrank, spine := wmVisibleRank(items, rolled, ranking.rank)

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
	wmDrawItems(&b, items, wmDraw{docURL: docURL, open: open, opts: opts}, func(it *wmItem) wmNodeStyle {
		rootDoc := landmarks && !strings.Contains(strings.TrimPrefix(it.doc.Path, "/"), "/")
		return wmNodeStyle{lod: vrank[it] >= wmLabelTop && !rootDoc && !it.ringed, orphan: ranking.degree[it.doc.Path] == 0}
	})
	b.WriteString(`</svg>`)
	if newURL != "" {
		// The branding desk shares the write gate with "new document" and the
		// same /w/<world>/ prefix, so it hangs off the same affordance.
		brandURL := newURL[:strings.LastIndex(newURL, "/new")] + "/branding"
		b.WriteString(`<p class="world-map-new"><a href="` + html.EscapeString(newURL) + `" hx-boost="false" class="edit-link">+ new document</a>` +
			` · <a href="` + html.EscapeString(brandURL) + `" hx-boost="false" class="edit-link">branding</a></p>`)
	}
	return template.HTML(b.String()) //nolint:gosec // built from escaped parts; all node text/attrs pass html.EscapeString
}

// wmRanking is how a world's documents rank against each other: each path's
// drawn-edge degree and its position in the layout order.
type wmRanking struct {
	degree map[string]int
	rank   map[string]int
}

// wmRankDocs ranks documents by degree (hubs first), then importance, then
// path, and counts the linked ones. Connectivity is by drawn edges, not the
// hub's orphan verdict: keying off d.Orphan silently flags nothing whenever
// the durable hub graph is sparse. Deterministic, so the layout is cacheable.
func wmRankDocs(docs []domain.FloorDoc, edges []domain.Edge) (ranking wmRanking, linked int) {
	degree := make(map[string]int, len(docs))
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
	rank := make(map[string]int, len(ordered))
	for i, d := range ordered {
		rank[d.Path] = i
		if degree[d.Path] > 0 {
			linked++
		}
	}
	return wmRanking{degree: degree, rank: rank}, linked
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
		directedEdge(b, e.from.end(), e.to.end(), edgeStyle{rel: e.rel, tier: tier, width: width})
	}
}

// wmDrawItems draws every visible item; style decides a document's label
// and orphan treatment.
func wmDrawItems(b *strings.Builder, items []*wmItem, draw wmDraw, style func(*wmItem) wmNodeStyle) {
	for _, it := range items {
		switch it.kind {
		case wmItemDoc:
			wmDocNode(b, it, draw.docURL, style(it))
		case wmItemGroup:
			wmAggNode(b, it, wmAgg{
				glyph:  "+",
				label:  it.group.key + " (" + strconv.Itoa(it.count) + ")",
				action: wmOpenExpand,
			}, draw)
		case wmItemMore:
			wmAggNode(b, it, wmAgg{
				glyph:  "…",
				label:  strconv.Itoa(it.count) + " more",
				action: wmOpenMore,
			}, draw)
		case wmItemAnchor:
			for _, c := range it.children {
				if c.hub { // the hub holds the centre; the anchor sits just above it
					it.y = c.y - c.r - it.r - 6
					break
				}
			}
			wmAggNode(b, it, wmAgg{glyph: "−", label: it.group.key, action: wmOpenCollapse}, draw)
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
func wmDocNode(b *strings.Builder, it *wmItem, docURL func(string) string, style wmNodeStyle) {
	doc := it.doc
	cls := "floor-doc status-" + doc.Status
	label := "floor-doc-label"
	if style.orphan {
		cls += " world-map-orphan"
	}
	if style.lod {
		label += " label-lod"
	}
	fmt.Fprintf(b, `<a href="%s" data-node="%s"><circle class="%s" cx="%d" cy="%d" r="%d"/>`,
		html.EscapeString(docURL(doc.Path)), html.EscapeString(doc.Path), html.EscapeString(cls), it.x, it.y, it.r)
	fmt.Fprintf(b, `<text class="%s" x="%d" y="%d" text-anchor="middle">%s</text>`,
		label, it.x, it.y+it.r+13, html.EscapeString(trimRunes(doc.Title, wmLabelTrim)))
	fmt.Fprintf(b, `<title>%s — %s</title></a>`, html.EscapeString(doc.Title), html.EscapeString(doc.Path))
}

// wmAggNode draws a group, "more" or anchor node. Its href is the directory
// listing (the no-JS and trail-pane behaviour); with an openURL the click is
// an htmx swap of the map fragment with the group expanded, paged or
// collapsed.
func wmAggNode(b *strings.Builder, it *wmItem, agg wmAgg, draw wmDraw) {
	cls := "floor-agg"
	switch it.kind {
	case wmItemMore:
		cls += " floor-agg-more"
	case wmItemAnchor:
		cls += " floor-agg-anchor"
	case wmItemDoc, wmItemGroup, wmItemRoot:
	}
	fmt.Fprintf(b, `<a href="%s" data-node="%s"`, html.EscapeString(draw.docURL(it.group.list)), html.EscapeString(it.id))
	if draw.opts.openURL != nil {
		fmt.Fprintf(b, ` hx-get="%s" hx-target="#map-canvas" hx-swap="innerHTML"`,
			html.EscapeString(draw.opts.openURL(draw.open.with(it.group.key, agg.action))))
	}
	fmt.Fprintf(b, `><circle class="%s" cx="%d" cy="%d" r="%d"/>`, cls, it.x, it.y, it.r)
	fmt.Fprintf(b, `<text class="floor-agg-glyph" x="%d" y="%d" text-anchor="middle">%s</text>`, it.x, it.y+4, agg.glyph)
	// An anchor's label goes above it: below is the group's centre (a hub or
	// the first spiral member) and its label.
	ly := it.y + it.r + 13
	if it.kind == wmItemAnchor {
		ly = it.y - it.r - 5
	}
	fmt.Fprintf(b, `<text class="floor-doc-label" x="%d" y="%d" text-anchor="middle">%s</text>`,
		it.x, ly, html.EscapeString(trimRunes(agg.label, wmLabelTrim+6)))
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

// floorSpineTitle names a floor-kind tombstone/spine pane: the universe term
// for the bare floor, "Map: <world>" for a world map.
func floorSpineTitle(addr paneAddr, terms Terms) string {
	if addr.World == "" {
		return terms.Universe
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
