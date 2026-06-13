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
)

// floorSVG renders the universe as one SVG. t/idx make the node links
// trail-aware: clicking a world opens its stacks, clicking a doc opens the
// document — both continue the trail from this pane.
func floorSVG(floor domain.Floor, t trail, idx int) template.HTML {
	n := len(floor.Worlds)
	if n == 0 {
		return template.HTML(`<p class="floor-empty">The universe is empty — no worlds visible to your identity.</p>`) //nolint:gosec // static markup
	}

	width := n * floorSystemW
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="floor" viewBox="0 0 %d %d" width="%d" height="%d" role="img" aria-label="universe map">`,
		width, floorSystemH, width, floorSystemH)

	for i, fw := range floor.Worlds {
		cx := i*floorSystemW + floorSystemW/2
		cy := floorSystemH / 2
		worldR := 30 + 2*len(fw.Docs)

		cls := "floor-system"
		if fw.Err {
			cls += " gone"
		}
		fmt.Fprintf(&b, `<g class="%s">`, cls)

		// The world node: clicking opens its stacks (root listing pane).
		worldHref := trailURL(trailAfterClick(t, idx, paneAddr{Kind: paneDoc, World: fw.World.Name, Value: "/"}))
		fmt.Fprintf(&b, `<a href="%s"><circle class="floor-world" cx="%d" cy="%d" r="%d"/>`,
			html.EscapeString(worldHref), cx, cy, worldR)
		fmt.Fprintf(&b, `<text class="floor-world-label" x="%d" y="%d" text-anchor="middle">%s</text>`,
			cx, cy+worldR+22, html.EscapeString(fw.World.Name))
		if fw.World.URL != "" {
			fmt.Fprintf(&b, `<title>%s</title>`, html.EscapeString(fw.World.URL))
		}
		b.WriteString(`</a>`)

		// Satellites: top-importance docs on the orbit ring, node size by
		// importance, stroke by status (the trust signal, again).
		for j, doc := range fw.Docs {
			angle := 2*math.Pi*float64(j)/float64(len(fw.Docs)) - math.Pi/2
			dx := cx + int(floorOrbitR*math.Cos(angle))
			dy := cy + int(floorOrbitR*math.Sin(angle))
			r := 5 + int(doc.Importance*9)

			docHref := trailURL(trailAfterClick(t, idx, paneAddr{Kind: paneDoc, World: fw.World.Name, Value: doc.Path}))
			fmt.Fprintf(&b, `<a href="%s"><circle class="floor-doc status-%s" cx="%d" cy="%d" r="%d"/>`,
				html.EscapeString(docHref), html.EscapeString(doc.Status), dx, dy, r)
			fmt.Fprintf(&b, `<text class="floor-doc-label" x="%d" y="%d" text-anchor="middle">%s</text>`,
				dx, dy+r+14, html.EscapeString(trimLabel(doc.Title)))
			fmt.Fprintf(&b, `<title>%s — %s</title></a>`,
				html.EscapeString(doc.Title), html.EscapeString(doc.Path))
		}
		b.WriteString(`</g>`)
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String()) //nolint:gosec // built here from escaped parts; node text/attrs all pass html.EscapeString
}

// trimLabel shortens a satellite label; the full title rides in <title>.
func trimLabel(s string) string {
	runes := []rune(s)
	if len(runes) <= floorLabelTrim {
		return s
	}
	return string(runes[:floorLabelTrim-1]) + "…"
}
