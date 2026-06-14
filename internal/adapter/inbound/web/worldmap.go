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
const (
	worldMapCols    = 3   // directory clusters per row
	worldMapCellW   = 320 // cell width per cluster
	worldMapCellH   = 320 // cell height per cluster
	worldMapOrbitR  = 96  // document ring radius within a cluster
	worldMapHubR    = 22  // directory hub node radius
	worldMapBubbleR = 18  // "+N more" aggregate node radius
	worldMapLabel   = 18  // node label length cap (full title in <title>)
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
		func(p string) string { return docRoute(world, p) })
	vm := page{
		Title:         "Map: " + world,
		Host:          world,
		Path:          "/",
		Content:       svg,
		World:         world,
		WorldPath:     url.PathEscape(world),
		Authenticated: c.Get(authedKey) != nil,
	}
	return c.Render(http.StatusOK, h.templateFor(c), vm)
}

// worldMapPaneView builds the world-map pane on the trail canvas (focused-live
// like every pane): nodes link to post-click trail URLs so a click continues
// the trail (decision 4). Like the floor, the map carries no margin — its
// signals are on the nodes (status strokes, importance sizing).
func (h *ReadingHandler) worldMapPaneView(ctx context.Context, t trail, i int, addr paneAddr) (paneVM, error) {
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
		func(p string) string {
			return trailURL(trailAfterClick(t, i, paneAddr{Kind: paneDoc, World: addr.World, Value: p}))
		})
	return vm, nil
}

// wmPlaced is a rendered document node with its computed position.
type wmPlaced struct {
	doc  domain.FloorDoc
	x, y int
	r    int
}

// worldMapSVG lays the directory clusters out in a deterministic grid: each
// cluster is a labeled hub with its documents on an orbit ring and an optional
// "+N more" aggregate. docURL turns a document path into its navigation target;
// listURL turns a directory listing path (the stacks) into one.
func worldMapSVG(wm domain.WorldMap, docURL, listURL func(string) string) template.HTML {
	if len(wm.Clusters) == 0 {
		return template.HTML(`<p class="floor-empty">This world's catalog is empty.</p>`) //nolint:gosec // static markup
	}

	cols := worldMapCols
	if len(wm.Clusters) < cols {
		cols = len(wm.Clusters)
	}
	rows := (len(wm.Clusters) + cols - 1) / cols
	width := cols * worldMapCellW
	height := rows * worldMapCellH

	// Pass 1: compute every document node's position, keyed by path, so edges
	// can be drawn before nodes (nodes on top) and only between drawn nodes.
	placed := make(map[string]wmPlaced)
	centers := make([]struct{ x, y int }, len(wm.Clusters))
	for ci, cl := range wm.Clusters {
		cx := (ci%cols)*worldMapCellW + worldMapCellW/2
		cy := (ci/cols)*worldMapCellH + worldMapCellH/2
		centers[ci] = struct{ x, y int }{cx, cy}
		slots := len(cl.Docs)
		if cl.More > 0 {
			slots++ // reserve a ring slot for the "+N more" aggregate
		}
		for j, doc := range cl.Docs {
			x, y := orbitAt(cx, cy, j, max(slots, 1))
			placed[doc.Path] = wmPlaced{doc: doc, x: x, y: y, r: 5 + int(doc.Importance*9)}
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="floor world-map" viewBox="0 0 %d %d" width="%d" height="%d" role="img" aria-label="%s map">`,
		width, height, width, height, html.EscapeString(wm.World.Name))

	// Intra-world edges first (both endpoints are labeled, so both are placed).
	for _, e := range wm.Edges {
		from, okF := placed[e.From.Path]
		to, okT := placed[e.To.Path]
		if !okF || !okT {
			continue
		}
		fmt.Fprintf(&b, `<line class="graph-edge" x1="%d" y1="%d" x2="%d" y2="%d"/>`, from.x, from.y, to.x, to.y)
	}

	// Pass 2: draw each cluster — its directory hub, document nodes, aggregate.
	for ci, cl := range wm.Clusters {
		worldMapCluster(&b, cl, centers[ci].x, centers[ci].y, placed, docURL, listURL)
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String()) //nolint:gosec // built from escaped parts; all node text/attrs pass html.EscapeString
}

// worldMapCluster renders one directory cluster: a central hub labeled with the
// directory name (links to its listing pane), the directory's documents on the
// ring, and a "+N more" aggregate node when the directory holds more than the
// rendered top.
func worldMapCluster(b *strings.Builder, cl domain.WorldCluster, cx, cy int, placed map[string]wmPlaced, docURL, listURL func(string) string) {
	fmt.Fprintf(b, `<g class="floor-system">`)

	// Directory hub — links to the listing pane (the stacks). The root cluster
	// ("") labels as "/".
	dirLabel := cl.Dir + "/"
	if cl.Dir == "" {
		dirLabel = "/"
	}
	fmt.Fprintf(b, `<a href="%s"><circle class="floor-world" cx="%d" cy="%d" r="%d"/>`,
		html.EscapeString(listURL(cl.ListPath)), cx, cy, worldMapHubR)
	fmt.Fprintf(b, `<text class="floor-world-label" x="%d" y="%d" text-anchor="middle">%s</text>`,
		cx, cy+worldMapHubR+18, html.EscapeString(dirLabel))
	fmt.Fprintf(b, `<title>%s</title></a>`, html.EscapeString(cl.ListPath))

	// Document nodes on the ring.
	for _, doc := range cl.Docs {
		pn := placed[doc.Path]
		fmt.Fprintf(b, `<a href="%s"><circle class="floor-doc status-%s" cx="%d" cy="%d" r="%d"/>`,
			html.EscapeString(docURL(doc.Path)), html.EscapeString(doc.Status), pn.x, pn.y, pn.r)
		fmt.Fprintf(b, `<text class="floor-doc-label" x="%d" y="%d" text-anchor="middle">%s</text>`,
			pn.x, pn.y+pn.r+13, html.EscapeString(trimWorldMapLabel(doc.Title)))
		fmt.Fprintf(b, `<title>%s — %s</title></a>`, html.EscapeString(doc.Title), html.EscapeString(doc.Path))
	}

	// "+N more" aggregate — the last ring slot, links to the listing pane.
	if cl.More > 0 {
		slots := len(cl.Docs) + 1
		x, y := orbitAt(cx, cy, len(cl.Docs), slots)
		fmt.Fprintf(b, `<a href="%s"><circle class="floor-doc world-map-more" cx="%d" cy="%d" r="%d"/>`,
			html.EscapeString(listURL(cl.ListPath)), x, y, worldMapBubbleR)
		fmt.Fprintf(b, `<text class="floor-doc-label" x="%d" y="%d" text-anchor="middle">+%d</text>`,
			x, y+4, cl.More)
		fmt.Fprintf(b, `<title>%d more in %s</title></a>`, cl.More, html.EscapeString(cl.ListPath))
	}
	b.WriteString(`</g>`)
}

// orbitAt places slot j of n on a ring around (cx, cy), starting at the top and
// going clockwise — deterministic, so the layout is stable and cacheable.
func orbitAt(cx, cy, j, n int) (int, int) {
	angle := 2*math.Pi*float64(j)/float64(n) - math.Pi/2
	return cx + int(worldMapOrbitR*math.Cos(angle)), cy + int(worldMapOrbitR*math.Sin(angle))
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
	if len(runes) <= worldMapLabel {
		return s
	}
	return string(runes[:worldMapLabel-1]) + "…"
}
