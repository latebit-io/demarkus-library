package web

import (
	"fmt"
	"html"
	"html/template"
	"math"
	"strings"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// The floor's SVG renderer (ADR 0005 decision 4; design in
// plans/reading-room.md §"Universe-view research"). Server-rendered SVG,
// deliberately: post-aggregation node counts are small, the layout is
// deterministic (stable across renders, cacheable, diffable), zero new JS —
// ADR 0003's canvas island stays unspent. Every node is a plain <a> whose
// href already carries its post-click trail URL, so walking the universe IS
// building a trail. The same data an agent reads via mark_worlds +
// mark_lookup("*") — the projection adds layout, never information.
const (
	floorSystemW   = 340 // horizontal slot per world cluster
	floorSystemH   = 440
	floorOrbitR    = 130 // satellite ring radius
	floorLabelTrim = 20  // satellite label length cap (full title in <title>)
	floorPortalW   = 150 // horizontal slot per portal node
	floorPortalH   = 120 // portal band height below the systems
	floorPortalR   = 16  // portal node radius
)

// floorPoint is a world cluster's (or portal's) center — the anchor edges
// connect to.
type floorPoint struct{ x, y int }

// floorSVG renders the universe as one SVG (plans "Floor enrichment"):
// authorized world clusters in a row, world-level edges between them, and
// externally-linked hosts as portal nodes in a band below. t/idx make every
// node link trail-aware — clicking a world opens its stacks, a doc opens the
// document, a portal opens that host's root — all continue the trail.
func floorSVG(floor domain.Floor, t trail, idx int) template.HTML {
	var systems, portals []domain.FloorWorld
	for _, fw := range floor.Worlds {
		if fw.Portal {
			portals = append(portals, fw)
		} else {
			systems = append(systems, fw)
		}
	}
	if len(systems) == 0 && len(portals) == 0 {
		return template.HTML(`<p class="floor-empty">The universe is empty — no worlds visible to your identity.</p>`) //nolint:gosec // static markup
	}

	// Lay out: systems across the top row, portals across a band below.
	// centers anchors the edges (keyed by world name).
	centers := make(map[string]floorPoint, len(floor.Worlds))
	for i, fw := range systems {
		centers[fw.World.Name] = floorPoint{x: i*floorSystemW + floorSystemW/2, y: floorSystemH / 2}
	}
	for i, fw := range portals {
		centers[fw.World.Name] = floorPoint{x: i*floorPortalW + floorPortalW/2, y: floorSystemH + floorPortalH/2}
	}

	width := max(len(systems)*floorSystemW, len(portals)*floorPortalW)
	height := floorSystemH
	if len(portals) > 0 {
		height += floorPortalH
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="floor" viewBox="0 0 %d %d" width="%d" height="%d" role="img" aria-label="universe map">`,
		width, height, width, height)

	// Edges first, so nodes draw on top. World-level lines between cluster
	// centers (durable hub graph ∪ observed-links map).
	for _, e := range floor.Edges {
		from, okF := centers[e.From.World]
		to, okT := centers[e.To.World]
		if !okF || !okT {
			continue
		}
		fmt.Fprintf(&b, `<line class="floor-edge" x1="%d" y1="%d" x2="%d" y2="%d"/>`, from.x, from.y, to.x, to.y)
	}

	for _, fw := range systems {
		floorSystem(&b, fw, centers[fw.World.Name], t, idx)
	}
	for _, fw := range portals {
		floorPortal(&b, fw, centers[fw.World.Name], t, idx)
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String()) //nolint:gosec // built here from escaped parts; node text/attrs all pass html.EscapeString
}

// floorSystem renders one authorized world: the world node (zooms into the
// world map) plus its top-importance documents as satellites on an orbit ring.
func floorSystem(b *strings.Builder, fw domain.FloorWorld, c floorPoint, t trail, idx int) {
	worldR := 30 + 2*len(fw.Docs)
	cls := "floor-system"
	if fw.Err {
		cls += " gone"
	}
	fmt.Fprintf(b, `<g class="%s">`, cls)

	// Clicking a world zooms one level in to its map (the world-view zoom
	// level); the stacks stay one click further via the map's dir aggregates.
	worldHref := trailURL(trailAfterClick(t, idx, paneAddr{Kind: paneFloor, World: fw.World.Name}))
	fmt.Fprintf(b, `<a href="%s"><circle class="floor-world" cx="%d" cy="%d" r="%d"/>`,
		html.EscapeString(worldHref), c.x, c.y, worldR)
	fmt.Fprintf(b, `<text class="floor-world-label" x="%d" y="%d" text-anchor="middle">%s</text>`,
		c.x, c.y+worldR+22, html.EscapeString(fw.World.Name))
	if fw.World.URL != "" {
		fmt.Fprintf(b, `<title>%s</title>`, html.EscapeString(fw.World.URL))
	}
	b.WriteString(`</a>`)

	for j, doc := range fw.Docs {
		angle := 2*math.Pi*float64(j)/float64(len(fw.Docs)) - math.Pi/2
		dx := c.x + int(floorOrbitR*math.Cos(angle))
		dy := c.y + int(floorOrbitR*math.Sin(angle))
		r := 5 + int(doc.Importance*9)

		docHref := trailURL(trailAfterClick(t, idx, paneAddr{Kind: paneDoc, World: fw.World.Name, Value: doc.Path}))
		fmt.Fprintf(b, `<a href="%s"><circle class="floor-doc status-%s" cx="%d" cy="%d" r="%d"/>`,
			html.EscapeString(docHref), html.EscapeString(doc.Status), dx, dy, r)
		fmt.Fprintf(b, `<text class="floor-doc-label" x="%d" y="%d" text-anchor="middle">%s</text>`,
			dx, dy+r+14, html.EscapeString(trimLabel(doc.Title)))
		fmt.Fprintf(b, `<title>%s — %s</title></a>`,
			html.EscapeString(doc.Title), html.EscapeString(doc.Path))
	}
	b.WriteString(`</g>`)
}

// floorPortal renders an externally-linked host (the extensional universe,
// ADR 0005 §16): a small rim node linking to that host's root. No satellites —
// the floor knows it exists from an edge, not from a catalog it can read.
func floorPortal(b *strings.Builder, fw domain.FloorWorld, c floorPoint, t trail, idx int) {
	href := trailURL(trailAfterClick(t, idx, paneAddr{Kind: paneDoc, World: fw.World.Name, Value: "/"}))
	fmt.Fprintf(b, `<g class="floor-portal"><a href="%s"><circle class="floor-portal-node" cx="%d" cy="%d" r="%d"/>`,
		html.EscapeString(href), c.x, c.y, floorPortalR)
	fmt.Fprintf(b, `<text class="floor-doc-label" x="%d" y="%d" text-anchor="middle">%s</text>`,
		c.x, c.y+floorPortalR+14, html.EscapeString(trimLabel(fw.World.Name)))
	fmt.Fprintf(b, `<title>%s</title></a></g>`, html.EscapeString(fw.World.Name))
}

// trimLabel shortens a satellite label; the full title rides in <title>.
func trimLabel(s string) string {
	runes := []rune(s)
	if len(runes) <= floorLabelTrim {
		return s
	}
	return string(runes[:floorLabelTrim-1]) + "…"
}
