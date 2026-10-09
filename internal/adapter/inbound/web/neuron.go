package web

import (
	"fmt"
	"html"
	"math"
	"strings"
)

// The drawing primitives every graph, map and floor SVG shares. The drawing
// is a neuron plate in the room's ink: a document is a soma with a dendritic
// arbor (a hub a pyramidal cell with a nucleus and an apical dendrite), a
// reference an axon that curves to a synaptic bouton on its target. The
// paint servers they reference (#synapse, #ganglion) live once in the page
// shell (templates/page.html, svg-defs).

// svgPoint is a position in SVG user units.
type svgPoint struct{ x, y float64 }

// polar is the point at distance r from (cx, cy) along angle a.
func polar(cx, cy, r, a float64) svgPoint {
	return svgPoint{cx + r*math.Cos(a), cy + r*math.Sin(a)}
}

// seededNoise returns a deterministic noise source for key: noise(i) is a
// stable value in [0, 1) per index, so a shape drawn from it survives
// re-renders unchanged while no two keys share one.
func seededNoise(key string) func(i int) float64 {
	seed := uint32(2166136261)
	for i := 0; i < len(key); i++ {
		seed = (seed ^ uint32(key[i])) * 16777619
	}
	return func(i int) float64 { return float64((seed*uint32(i+1)*2654435761)>>22) / 1023 }
}

// edgeEnd is one end of a drawn edge. id is what a hover handler matches its
// incident edges on.
type edgeEnd struct {
	x, y int
	r    int
	id   string
}

// edgeStyle is a drawn edge's treatment. Empty fields are the plain
// reference edge; the world map sets tier to quiet its hairball.
type edgeStyle struct {
	rel   string    // typed relation's predicate: draws dashed, tooltipped
	tier  string    // rest-state class: edge-spine, edge-tree, edge-dim
	width float64   // > 0 overrides stroke width (a rolled-up bundle)
	via   *svgPoint // control point of the curve; nil bends it gently rightward
}

// edgeBend is the default curvature: a fraction of the edge length, clamped
// so short edges still arc and long ones do not swing wide.
const edgeBend = 0.16

// edgeVia is the control point an edge curves through: the caller's, else
// the midpoint pushed to the right of travel (so a pair of opposite edges
// bends apart like two lanes). The endpoints must differ.
func edgeVia(from, to edgeEnd, style edgeStyle) svgPoint {
	if style.via != nil {
		return *style.via
	}
	dx, dy := float64(to.x-from.x), float64(to.y-from.y)
	d := math.Hypot(dx, dy)
	bend := math.Min(70, math.Max(8, d*edgeBend))
	return svgPoint{x: float64(from.x+to.x)/2 - dy/d*bend, y: float64(from.y+to.y)/2 + dx/d*bend}
}

// directedEdge draws a reference edge as an axon: a quadratic curve from the
// source, trimmed back by each endpoint's radius along the curve's end
// tangents, ending in a synaptic bouton (the stylesheet's marker) just
// outside the target node.
func directedEdge(b *strings.Builder, from, to edgeEnd, style edgeStyle) {
	if from.x == to.x && from.y == to.y {
		return
	}
	via := edgeVia(from, to, style)
	const gap = 3.0 // breathing room between bouton and target rim
	start := trimTowards(svgPoint{float64(from.x), float64(from.y)}, via, float64(from.r))
	end := trimTowards(svgPoint{float64(to.x), float64(to.y)}, via, float64(to.r)+gap)
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
	fmt.Fprintf(b, `<path class="%s" d="M%.0f,%.0f Q%.0f,%.0f %.0f,%.0f" data-from="%s" data-to="%s"%s>`,
		cls, start.x, start.y, via.x, via.y, end.x, end.y, html.EscapeString(from.id), html.EscapeString(to.id), stroke)
	if style.rel != "" {
		fmt.Fprintf(b, `<title>%s</title>`, html.EscapeString(style.rel))
	}
	b.WriteString(`</path>`)
}

// trimTowards moves p a distance d towards target, or not at all when they
// coincide.
func trimTowards(p, target svgPoint, d float64) svgPoint {
	dx, dy := target.x-p.x, target.y-p.y
	if l := math.Hypot(dx, dy); l > 0 {
		return svgPoint{p.x + dx/l*d, p.y + dy/l*d}
	}
	return p
}

// somaSpec places a soma's dendritic arbor: short branching processes around
// the rim, seeded by the document so the shape is stable, kept out of the
// sector its label occupies, and grown into a pyramidal cell for a hub.
type somaSpec struct {
	x, y, r int
	seed    string
	label   float64 // direction of the label, radians
	hub     bool
}

const (
	dendriteCount  = 3    // arbor processes on a small soma; a hub gets two more
	dendriteClear  = 0.95 // half-angle kept clear around the label, radians
	dendriteTrunk  = 0.95 // trunk length as a share of the radius
	dendriteApical = 2.1  // a hub's apical dendrite, the pyramidal cell's long one
	dendriteBranch = 0.6  // branch length as a share of the radius
	dendriteFork   = 0.55 // branch spread from the trunk, radians
)

var dendriteSides = [2]float64{-1, 1}

// dendrites draws the arbor as one path, straight into b.
func dendrites(b *strings.Builder, s somaSpec) {
	noise := seededNoise(s.seed)
	n, cls := dendriteCount, "dendrite"
	if s.hub {
		n, cls = dendriteCount+2, "dendrite dendrite-hub"
	}
	spin := noise(99) * 2 * math.Pi
	fmt.Fprintf(b, `<path class="%s" d="`, cls)
	for i := 0; i < n; i++ {
		a := 2*math.Pi*(float64(i)+noise(i)*0.6)/float64(n) + spin
		// Swing a process that would cross the label to the sector's edge.
		if off := math.Remainder(a-s.label, 2*math.Pi); math.Abs(off) < dendriteClear {
			a += math.Copysign(dendriteClear-math.Abs(off)+0.1, off)
		}
		dendrite(b, s, noise, a, i, s.hub && i == 0)
	}
	b.WriteString(`"/>`)
}

// dendrite appends one process at angle a: a trunk bowed by a seeded offset
// forking into two branches. The apical process is longer and forks twice,
// with a side branch halfway up.
func dendrite(b *strings.Builder, s somaSpec, noise func(int) float64, a float64, i int, apical bool) {
	cx, cy, r := float64(s.x), float64(s.y), float64(s.r)
	trunk := r * (dendriteTrunk + noise(i+10)*0.5)
	if apical {
		trunk = r * dendriteApical
	}
	bow := (noise(i+20) - 0.5) * r * 0.6
	start := polar(cx, cy, r, a)
	mid := polar(cx, cy, r+trunk/2, a)
	mid.x, mid.y = mid.x-bow*math.Sin(a), mid.y+bow*math.Cos(a)
	tip := polar(cx, cy, r+trunk, a)
	fmt.Fprintf(b, "M%.0f,%.0f Q%.0f,%.0f %.0f,%.0f", start.x, start.y, mid.x, mid.y, tip.x, tip.y)
	for _, side := range dendriteSides {
		ba := a + side*(dendriteFork+noise(i+30)*0.3)
		bl := r * (dendriteBranch + noise(i+40)*0.4)
		branch := polar(tip.x, tip.y, bl, ba)
		fmt.Fprintf(b, " M%.0f,%.0f L%.0f,%.0f", tip.x, tip.y, branch.x, branch.y)
		if !apical {
			continue
		}
		fork := polar(branch.x, branch.y, bl*0.7, ba+side*0.5)
		fmt.Fprintf(b, " M%.0f,%.0f L%.0f,%.0f", branch.x, branch.y, fork.x, fork.y)
		half := polar(cx, cy, r+trunk*0.55, a)
		twig := polar(half.x, half.y, bl, a+side*1.1)
		fmt.Fprintf(b, " M%.0f,%.0f L%.0f,%.0f", half.x, half.y, twig.x, twig.y)
	}
}

// nucleus draws the inner body that marks a soma as a cell rather than a dot.
func nucleus(b *strings.Builder, cx, cy, r int) {
	fmt.Fprintf(b, `<circle class="nucleus" cx="%d" cy="%d" r="%d"/>`, cx, cy, max(2, r*2/5))
}
