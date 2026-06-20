package web

import (
	"context"
	"fmt"
	"html"
	"html/template"
	"math"
	"net/http"
	"net/url"
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
	wmWidth       = 720 // canvas width
	wmRingMin     = 120 // connected-ring radius floor
	wmRingMax     = 280 // connected-ring radius ceiling
	wmLabelTrim   = 18  // node label length cap (full title in <title>)
	wmOrphanCols  = 6   // orphan-band columns
	wmOrphanCellW = 116
	wmOrphanCellH = 62
)

// WorldMapPage renders a world's map as a standalone permalink — /w/:world/u —
// the chunk-tail source and projection escape (ADR 0005 decision 12). On the
// canvas the same map renders as a trail pane.
func (h *ReadingHandler) WorldMapPage(c *echo.Context) error {
	world := c.Param("world")
	wm, err := h.reading.WorldMap(c.Request().Context(), world)
	if err != nil {
		return presentError(c, err, world, "/")
	}
	// Single-pane permalink: nodes link to /w/ permalinks.
	svg := worldMapSVG(wm,
		func(p string) string { return docRoute(world, p) },
		worldNewURL(world, c.Get(authedKey) != nil))
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
// §5): linked documents (≥1 reference edge, per the durable hub graph) in a
// connected ring with their reference edges, orphans floated apart in a band
// below — the one signal the index can't give. A caption tallies linked vs
// orphan. docURL turns a document path into its navigation target; newURL, when
// non-empty, adds the "new document" affordance (the only entry point for an
// empty world, where there is no doc margin to host the usual "new" link).
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

	var linked, orphans []domain.FloorDoc
	for _, d := range docs {
		if d.Orphan {
			orphans = append(orphans, d)
		} else {
			linked = append(linked, d)
		}
	}

	// Connected ring: linked docs evenly spaced, radius growing with count.
	ringR := min(max(wmRingMin, 14*len(linked)), wmRingMax)
	cx, cy := wmWidth/2, 40+ringR
	placed := make(map[string]wmPlaced, len(linked))
	for j, d := range linked {
		x, y := ringAt(cx, cy, j, max(len(linked), 1), ringR)
		placed[d.Path] = wmPlaced{doc: d, x: x, y: y, r: 5 + int(d.Importance*9)}
	}
	height := cy + ringR + 36

	// Orphan band below, floated apart (no edges) — visually separate.
	orphanTop := height + 28
	if len(orphans) > 0 {
		rows := (len(orphans) + wmOrphanCols - 1) / wmOrphanCols
		height = orphanTop + rows*wmOrphanCellH + 12
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="floor world-map" viewBox="0 0 %d %d" width="%d" height="%d" role="img" aria-label="%s map">`,
		wmWidth, height, wmWidth, height, html.EscapeString(wm.World.Name))
	fmt.Fprintf(&b, `<text class="world-map-caption" x="%d" y="22" text-anchor="middle">%d linked · %d orphan</text>`,
		wmWidth/2, len(linked), len(orphans))

	// Reference edges first (nodes draw on top), only between placed (linked) nodes.
	for _, e := range wm.Edges {
		from, okF := placed[e.From.Path]
		to, okT := placed[e.To.Path]
		if !okF || !okT {
			continue
		}
		fmt.Fprintf(&b, `<line class="graph-edge" x1="%d" y1="%d" x2="%d" y2="%d"/>`, from.x, from.y, to.x, to.y)
	}
	for _, d := range linked {
		pn := placed[d.Path]
		wmDocNode(&b, d, pn.x, pn.y, pn.r, docURL, false)
	}

	if len(orphans) > 0 {
		fmt.Fprintf(&b, `<text class="world-map-band" x="20" y="%d">orphans</text>`, orphanTop-10)
		for j, d := range orphans {
			x := (j%wmOrphanCols)*wmOrphanCellW + wmOrphanCellW/2
			y := orphanTop + (j/wmOrphanCols)*wmOrphanCellH + wmOrphanCellH/2
			wmDocNode(&b, d, x, y, 6, docURL, true)
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
	if orphan {
		cls += " world-map-orphan"
	}
	fmt.Fprintf(b, `<a href="%s"><circle class="%s" cx="%d" cy="%d" r="%d"/>`,
		html.EscapeString(docURL(doc.Path)), html.EscapeString(cls), x, y, r)
	fmt.Fprintf(b, `<text class="floor-doc-label" x="%d" y="%d" text-anchor="middle">%s</text>`,
		x, y+r+13, html.EscapeString(trimWorldMapLabel(doc.Title)))
	fmt.Fprintf(b, `<title>%s — %s</title></a>`, html.EscapeString(doc.Title), html.EscapeString(doc.Path))
}

// ringAt places slot j of n on a ring of radius r around (cx, cy), starting at
// the top and going clockwise — deterministic, so the layout is cacheable.
func ringAt(cx, cy, j, n, r int) (x, y int) {
	angle := 2*math.Pi*float64(j)/float64(n) - math.Pi/2
	return cx + int(float64(r)*math.Cos(angle)), cy + int(float64(r)*math.Sin(angle))
}

// floorSpineTitle names a floor-kind tombstone/spine pane: "Universe" for the
// bare floor, "Map: <world>" for a world map.
func floorSpineTitle(addr paneAddr) string {
	if addr.World == "" {
		return "Universe"
	}
	return "Map: " + addr.World
}

func trimWorldMapLabel(s string) string {
	runes := []rune(s)
	if len(runes) <= wmLabelTrim {
		return s
	}
	return string(runes[:wmLabelTrim-1]) + "…"
}
