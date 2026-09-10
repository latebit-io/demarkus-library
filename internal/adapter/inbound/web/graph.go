package web

import (
	"fmt"
	"html"
	"html/template"
	"math"
	"strings"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// The graph neighborhood renderer (R3; ADR 0005 decisions 4/5). One document
// with its observed outbound links and backlinks, drawn as SSR SVG — same
// approach as the floor, so ADR 0003's canvas island stays unspent. Every node
// is a plain <a> whose href continues the trail (walking the graph IS building
// a trail). Edges come from the render-time observed-links map, so the view
// works in both transports and is simply sparse until the documents are read.
// The pane and the standalone page that call this live in spatial_panes.go and
// spatial_handlers.go.
const (
	graphLabel    = 22 // neighbor label length cap
	graphCenterR  = 9
	graphNodeR    = 6
	graphRatio    = 1.5 // x:y stretch — a wide neighborhood fills a wide overlay
	graphNodeVGap = 46  // vertical spacing budget per neighbor on an arc
	graphMinRy    = 120 // min vertical arc radius (small neighborhoods stay legible)
	graphMaxRy    = 440 // cap so a huge neighborhood doesn't run away
	graphLabelPad = 150 // horizontal room for the outward (left/right) node labels
	graphVPad     = 60  // top/bottom room for labels
	graphMaxSide  = 18  // nodes per arc; past this graphMaxRy's per-node budget collapses into label overlap
)

// arrowMarker is the <defs> block defining the directional edge arrowhead,
// emitted once at the top of each graph / world-map SVG so a reference edge
// reads From→To. markerUnits="userSpaceOnUse" keeps the head a fixed size
// regardless of the (thin) edge stroke width.
// arrowMarker defines two heads: the resting #arrow and the green #arrow-hot the
// hover state swaps in (islands.js adds .edge-hot to a hovered node's edges).
const arrowMarker = `<defs><marker id="arrow" markerWidth="9" markerHeight="9" refX="8" refY="4.5" orient="auto" markerUnits="userSpaceOnUse"><path class="edge-arrow" d="M0,0 L9,4.5 L0,9 z"/></marker><marker id="arrow-hot" markerWidth="9" markerHeight="9" refX="8" refY="4.5" orient="auto" markerUnits="userSpaceOnUse"><path class="edge-arrow-hot" d="M0,0 L9,4.5 L0,9 z"/></marker></defs>`

// edgeEnd is one end of a drawn edge. id is what a hover handler matches its
// incident edges on.
type edgeEnd struct {
	x, y int
	r    int
	id   string
}

// edgeStyle is a drawn edge's non-geometric treatment. Empty fields are the
// plain reference edge; the world map sets tier to quiet its hairball.
type edgeStyle struct {
	rel   string  // typed relation's predicate: draws dashed, tooltipped
	tier  string  // rest-state class: edge-spine, edge-tree, edge-dim
	width float64 // > 0 overrides stroke width (a rolled-up bundle)
}

// directedEdge draws a reference edge as an arrow at the target, trimmed back
// by each endpoint's radius so the head lands outside the target node rather
// than under it.
func directedEdge(b *strings.Builder, from, to edgeEnd, style edgeStyle) {
	dx, dy := float64(to.x-from.x), float64(to.y-from.y)
	d := math.Hypot(dx, dy)
	if d == 0 {
		return
	}
	ux, uy := dx/d, dy/d
	const gap = 3.0 // breathing room between arrow tip and target rim
	sx, sy := from.x+int(ux*float64(from.r)), from.y+int(uy*float64(from.r))
	ex, ey := to.x-int(ux*(float64(to.r)+gap)), to.y-int(uy*(float64(to.r)+gap))
	cls := "graph-edge"
	if style.tier != "" {
		cls += " " + style.tier
	}
	if style.rel != "" {
		cls += " edge-rel"
	}
	stroke := ""
	if style.width > 0 {
		stroke = fmt.Sprintf(` style="stroke-width:%.1f"`, style.width)
	}
	fmt.Fprintf(b, `<line class="%s" x1="%d" y1="%d" x2="%d" y2="%d" data-from="%s" data-to="%s" marker-end="url(#arrow)"%s>`,
		cls, sx, sy, ex, ey, html.EscapeString(from.id), html.EscapeString(to.id), stroke)
	if style.rel != "" {
		fmt.Fprintf(b, `<title>%s</title>`, html.EscapeString(style.rel))
	}
	b.WriteString(`</line>`)
}

// graphSVG lays out the neighborhood deterministically (server-side, no client
// physics): the center document in the middle, its neighbors on a ring —
// backlinks on the left arc, outbound links on the right — each joined to the
// center by an edge. urlFor turns each ref into its navigation target.
func graphSVG(n domain.Neighborhood, urlFor func(domain.Ref) string, onTrail map[domain.Ref]bool) template.HTML {
	if len(n.In) == 0 && len(n.Out) == 0 {
		return template.HTML(`<p class="graph-empty">No links observed yet — the neighborhood fills in as connected documents are read.</p>`) //nolint:gosec // static markup
	}
	// High-degree hubs are capped per arc (refs arrive deterministically
	// sorted, so the cut is stable); the overflow is declared honestly as a
	// "+N more" note under the arc rather than drawn as overlapping labels.
	in, inMore := capSide(n.In)
	out, outMore := capSide(n.Out)
	// A wide elliptical neighborhood sized to its node count, viewBox fit tightly
	// so it fills the overlay instead of centering a small ring in a fixed box.
	// ry grows with the busier side (so arcs never crowd vertically); rx is
	// stretched wider; the canvas adds room for the outward node labels.
	maxSide := max(len(in), len(out))
	ry := min(max(graphMinRy, maxSide*graphNodeVGap/2), graphMaxRy)
	rx := int(float64(ry) * graphRatio)
	width, height := 2*rx+2*graphLabelPad, 2*ry+2*graphVPad
	cx, cy := width/2, height/2

	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="graph" viewBox="0 0 %d %d" width="%d" height="%d" role="img" aria-label="document neighborhood">`,
		width, height, width, height)
	b.WriteString(arrowMarker)

	// Place backlinks across the left half (π/2 … 3π/2) and outbound links
	// across the right half (-π/2 … π/2); a lone node sits at the pole.
	arc := ellipse{cx: cx, cy: cy, rx: rx, ry: ry}
	placed := append(arcNodes(in, arc, true), arcNodes(out, arc, false)...)

	// Edges first, so nodes draw on top. Direction follows the reference: an
	// outbound link points center→neighbor, a backlink points neighbor→center.
	for _, pn := range placed {
		if pn.inbound {
			directedEdge(&b,
				edgeEnd{x: pn.x, y: pn.y, r: graphNodeR, id: pn.ref.Path},
				edgeEnd{x: cx, y: cy, r: graphCenterR, id: n.Center.Path}, edgeStyle{})
		} else {
			directedEdge(&b,
				edgeEnd{x: cx, y: cy, r: graphCenterR, id: n.Center.Path},
				edgeEnd{x: pn.x, y: pn.y, r: graphNodeR, id: pn.ref.Path}, edgeStyle{})
		}
	}
	// Center node (data-node so hovering it lights up all its edges).
	fmt.Fprintf(&b, `<circle class="graph-center" data-node="%s" cx="%d" cy="%d" r="%d"/>`,
		html.EscapeString(n.Center.Path), cx, cy, graphCenterR)
	fmt.Fprintf(&b, `<text class="graph-center-label" x="%d" y="%d" text-anchor="middle">%s</text>`,
		cx, cy-graphCenterR-8, html.EscapeString(refTitle(n.Center)))
	// Neighbor nodes.
	for _, pn := range placed {
		dir := "in"
		if !pn.inbound {
			dir = "out"
		}
		cls := "graph-node graph-" + dir
		if onTrail[pn.ref] {
			cls += " graph-walked" // a neighbor already on your trail
		}
		fmt.Fprintf(&b, `<a href="%s" data-node="%s"><circle class="%s" cx="%d" cy="%d" r="%d"/>`,
			html.EscapeString(urlFor(pn.ref)), html.EscapeString(pn.ref.Path), html.EscapeString(cls), pn.x, pn.y, graphNodeR)
		anchor := "middle"
		if pn.x < cx {
			anchor = "end"
		} else if pn.x > cx {
			anchor = "start"
		}
		fmt.Fprintf(&b, `<text class="graph-node-label" x="%d" y="%d" text-anchor="%s">%s</text>`,
			pn.x, pn.y-graphNodeR-6, anchor, html.EscapeString(trimGraphLabel(refTitle(pn.ref))))
		fmt.Fprintf(&b, `<title>%s — %s</title></a>`, html.EscapeString(pn.ref.Path), html.EscapeString(pn.ref.World))
	}
	if inMore > 0 {
		fmt.Fprintf(&b, `<text class="graph-more" x="%d" y="%d" text-anchor="middle">+%d more backlinks</text>`,
			cx-rx/2, height-12, inMore)
	}
	if outMore > 0 {
		fmt.Fprintf(&b, `<text class="graph-more" x="%d" y="%d" text-anchor="middle">+%d more links</text>`,
			cx+rx/2, height-12, outMore)
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String()) //nolint:gosec // built from escaped parts; all text/attrs pass html.EscapeString
}

// capSide trims one arc's refs to graphMaxSide, reporting how many were cut.
func capSide(refs []domain.Ref) (kept []domain.Ref, cut int) {
	if len(refs) <= graphMaxSide {
		return refs, 0
	}
	return refs[:graphMaxSide], len(refs) - graphMaxSide
}

// placedNode is a neighbor with its computed position.
type placedNode struct {
	ref     domain.Ref
	x, y    int
	inbound bool
}

// arcNodes spreads refs over half of arc: the left half for backlinks, the
// right half for outbound links. A single node sits on the pole of its side.
func arcNodes(refs []domain.Ref, arc ellipse, inbound bool) []placedNode {
	out := make([]placedNode, 0, len(refs))
	for j, r := range refs {
		var frac float64
		if len(refs) > 1 {
			frac = float64(j) / float64(len(refs)-1)
		} else {
			frac = 0.5
		}
		// Sweep top→bottom across the half; mirror to the correct side.
		angle := -math.Pi/2 + frac*math.Pi
		if inbound {
			angle = math.Pi - angle
		}
		out = append(out, placedNode{
			ref:     r,
			x:       arc.cx + int(float64(arc.rx)*math.Cos(angle)),
			y:       arc.cy + int(float64(arc.ry)*math.Sin(angle)),
			inbound: inbound,
		})
	}
	return out
}

// trailDocRefs is the set of document refs on the trail — the graph overlay
// marks these neighbors as already-walked (ADR 0006 §4).
func trailDocRefs(t trail) map[domain.Ref]bool {
	refs := map[domain.Ref]bool{}
	for _, p := range t.Panes {
		if p.Kind == paneDoc && !domain.IsListingPath(p.Value) {
			refs[domain.Ref{World: p.World, Path: p.Value}] = true
		}
	}
	return refs
}

func trimGraphLabel(s string) string {
	runes := []rune(s)
	if len(runes) <= graphLabel {
		return s
	}
	return string(runes[:graphLabel-1]) + "…"
}
