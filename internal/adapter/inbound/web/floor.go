package web

import (
	"fmt"
	"html"
	"html/template"
	"math"
	"sort"
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
	floorPortalR = 12 // portal node radius (an external host, no catalog)
)

// floorSVG renders the universe as one SVG in the world map's grammar
// (plans/world-map-aggregation.md): every authorized world is an aggregate
// node sized by its catalog sample, portals are small rim nodes, world-level
// edges roll up with width by count, and the layout is the leaf-group rule
// (a ring up to wmRingMax worlds around the hub world, a sunflower beyond).
// t/idx make every node link trail-aware — a world opens its stacks, a
// portal that host's root — so walking the universe IS building a trail.
func floorSVG(floor domain.Floor, t trail, idx int) template.HTML {
	if len(floor.Worlds) == 0 {
		return template.HTML(`<p class="floor-empty">The universe is empty — no worlds visible to your identity.</p>`) //nolint:gosec // static markup
	}
	items := make([]*wmItem, 0, len(floor.Worlds))
	byName := make(map[string]*wmItem, len(floor.Worlds))
	systems, portals := 0, 0
	for i := range floor.Worlds {
		fw := &floor.Worlds[i]
		it := &wmItem{id: "w:" + fw.World.Name, kind: wmItemGroup, count: len(fw.Docs),
			group: &wmGroup{key: fw.World.Name, name: fw.World.Name, list: "/"}}
		if fw.Portal {
			it.r = floorPortalR
			portals++
		} else {
			it.r = wmAggRadius(it.count) + 6
			systems++
		}
		it.foot = float64(it.r) + wmPitch/2
		items = append(items, it)
		byName[fw.World.Name] = it
	}
	rolled := make([]*wmRolled, 0, len(floor.Edges))
	for _, e := range floor.Edges {
		from, okF := byName[e.From.World]
		to, okT := byName[e.To.World]
		if !okF || !okT || from == to {
			continue
		}
		rolled = append(rolled, &wmRolled{from: from, to: to, count: max(e.Count, 1)})
	}
	vrank, spine := wmVisibleRank(items, rolled, nil)
	byRank := append([]*wmItem(nil), items...)
	sort.SliceStable(byRank, func(i, j int) bool { return vrank[byRank[i]] < vrank[byRank[j]] })
	// The hub world holds the centre when it links to at least half the others.
	if len(items) > 2 {
		hub := byRank[0]
		linked := map[*wmItem]bool{}
		for _, e := range rolled {
			if e.from == hub {
				linked[e.to] = true
			}
			if e.to == hub {
				linked[e.from] = true
			}
		}
		hub.hub = len(linked) >= (len(items)-1)/2
	}
	outerRy := floorLayout(byRank)
	outerRx := int(float64(outerRy) * wmTierRatio)
	width := max(2*outerRx+2*wmSideMargin, wmMinWidth)
	height := wmTierTop + 2*outerRy + 36
	floorPlace(byRank, width/2, wmTierTop+outerRy)

	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="floor" viewBox="0 0 %d %d" width="%d" height="%d" role="img" aria-label="universe map">`,
		width, height, width, height)
	fmt.Fprintf(&b, `<text class="world-map-caption" x="%d" y="22" text-anchor="middle">%s · %s</text>`,
		width/2, plural(systems, "world"), plural(portals, "portal"))
	b.WriteString(arrowMarker)
	wmDrawEdges(&b, rolled, vrank, spine)
	for i := range floor.Worlds {
		floorWorldNode(&b, &floor.Worlds[i], byName[floor.Worlds[i].World.Name], t, idx)
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String()) //nolint:gosec // built here from escaped parts; node text/attrs all pass html.EscapeString
}

// plural formats a count with its noun.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// floorLayout returns the vertical radius the universe needs: a ring for up
// to wmRingMax worlds (the hub, if any, at the centre), a sunflower beyond.
// The ring keeps the largest world's diameter plus padding between
// neighbours.
func floorLayout(byRank []*wmItem) int {
	n, maxR := 0, 0
	for _, it := range byRank {
		maxR = max(maxR, it.r)
		if !it.hub {
			n++
		}
	}
	if len(byRank) > wmRingMax {
		return wmSpiralRadius(len(byRank)) + maxR
	}
	ry := math.Max(wmRingRadius(n), float64(n)*float64(2*maxR+wmAggPad)/(2*math.Pi))
	return int(ry) + maxR + 24
}

// floorPlace positions the worlds around (cx, cy) per floorLayout.
func floorPlace(byRank []*wmItem, cx, cy int) {
	if len(byRank) > wmRingMax {
		next := 0
		for _, it := range byRank {
			if it.hub {
				it.x, it.y = cx, cy
				next = max(next, 1)
				continue
			}
			it.x, it.y = spiralAt(cx, cy, next)
			next++
		}
		return
	}
	n, maxR := 0, 0
	for _, it := range byRank {
		maxR = max(maxR, it.r)
		if !it.hub {
			n++
		}
	}
	ry := int(math.Max(wmRingRadius(n), float64(n)*float64(2*maxR+wmAggPad)/(2*math.Pi)))
	rx := int(float64(ry) * wmTierRatio)
	j := 0
	for _, it := range byRank {
		if it.hub {
			it.x, it.y = cx, cy
			continue
		}
		it.x, it.y = ellipseAt(cx, cy, j, n, rx, ry)
		j++
	}
}

// floorWorldNode draws one world: an aggregate circle (a dashed rim node for
// a portal, dimmed when unreadable) that enters the world at its stacks, its
// name, and its URL and sampled size in the tooltip.
func floorWorldNode(b *strings.Builder, fw *domain.FloorWorld, it *wmItem, t trail, idx int) {
	href := trailURL(trailAfterClick(t, idx, paneAddr{Kind: paneDoc, World: fw.World.Name, Value: "/"}))
	cls, label := "floor-world", "floor-world-label"
	if fw.Portal {
		cls, label = "floor-portal-node", "floor-doc-label"
	}
	if fw.Err {
		cls += " gone"
	}
	fmt.Fprintf(b, `<a href="%s" data-node="%s"><circle class="%s" cx="%d" cy="%d" r="%d"/>`,
		html.EscapeString(href), html.EscapeString(it.id), cls, it.x, it.y, it.r)
	fmt.Fprintf(b, `<text class="%s" x="%d" y="%d" text-anchor="middle">%s</text>`,
		label, it.x, it.y+it.r+16, html.EscapeString(trimRunes(fw.World.Name, wmLabelTrim+6)))
	title := fw.World.URL
	if title == "" {
		title = fw.World.Name
	}
	if !fw.Portal {
		title += fmt.Sprintf(" — %d documents", it.count)
	}
	fmt.Fprintf(b, `<title>%s</title></a>`, html.EscapeString(title))
}

// floorCards renders the universe as worlds-only "door" cards (ADR 0006 §5):
// the universe lists worlds, never loose documents. Each card reads as a door at
// rest — a container glyph, the world name, a trailing chevron — affordances a
// document row never has. Federated/remote worlds (portals) get a dashed,
// external-link treatment: visibly "a server you connect to," not a local world
// you enter. This is the default cold-entry view; floorSVG is the "view as map".
func floorCards(floor domain.Floor, t trail, idx int) template.HTML {
	if len(floor.Worlds) == 0 {
		return template.HTML(`<p class="floor-empty">The universe is empty — no worlds visible to your identity.</p>`) //nolint:gosec // static markup
	}
	var b strings.Builder
	b.WriteString(`<ul class="worlds">`)
	for _, fw := range floor.Worlds {
		cls := "world-card"
		// Entering a world lands on its stacks — the root listing, rendered as
		// the rich title-first index (ADR 0006 §5). That is the nav surface; the
		// world map is discovery-only, summoned as the `m` overlay. (Both local
		// and federated worlds enter at the root listing.)
		href := trailURL(trailAfterClick(t, idx, paneAddr{Kind: paneDoc, World: fw.World.Name, Value: "/"}))
		if fw.Portal {
			cls += " federated"
		}
		if fw.Err {
			cls += " gone"
		}
		fmt.Fprintf(&b, `<li><a class="%s" href="%s">`, cls, html.EscapeString(href))
		b.WriteString(`<span class="world-glyph" aria-hidden="true">▤</span>`)
		fmt.Fprintf(&b, `<span class="world-name">%s</span>`, html.EscapeString(fw.World.Name))
		switch {
		case fw.Portal:
			b.WriteString(`<span class="world-tag">federated · sign-in</span><span class="world-chev" aria-hidden="true">↗</span>`)
		case fw.Err:
			b.WriteString(`<span class="world-tag">unreadable</span>`)
		default:
			b.WriteString(`<span class="world-chev" aria-hidden="true">›</span>`)
		}
		b.WriteString(`</a></li>`)
	}
	b.WriteString(`</ul>`)
	return template.HTML(b.String()) //nolint:gosec // built from html.EscapeString'd parts only
}

// floorViewToggle is the "view as map / worlds" switch (ADR 0006 §5): the
// universe is worlds-by-default, with the federation topology a deliberate
// secondary view. With JS the "view as map" link summons the full-viewport
// universe overlay (ADR 0006 §6; islands.js intercepts the universe-open
// class); with JS off the same href renders the map inline on the floor pane
// via ?view=map, so the topology stays reachable either way.
func floorViewToggle(t trail, mapView bool) template.HTML {
	base := trailURL(t)
	if mapView {
		return template.HTML(`<a class="floor-view" href="` + html.EscapeString(base) + `">← worlds</a>`) //nolint:gosec // escaped
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	// hx-boost="false" so htmx doesn't boost-navigate the click — islands.js
	// intercepts it to summon the overlay; the href is only the JS-off fallback.
	return template.HTML(`<a class="floor-view universe-open" href="` + html.EscapeString(base+sep+"view=map") + `" hx-boost="false">view as map →</a>`) //nolint:gosec // escaped
}
