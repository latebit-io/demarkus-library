package web

import (
	"fmt"
	"html"
	"math"
	"strings"
)

// The drawing primitives every graph, map and floor SVG shares: a document is
// a soma with a dendritic arbor, a reference an axon ending in a bouton. Their
// paint servers (#synapse, #ganglion) live once in the page shell (svg-defs).

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

// directedEdge draws a reference edge as an axon: a quadratic curve trimmed
// back by each endpoint's radius along its end tangents, so the stylesheet's
// bouton lands just outside the target node.
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
		dendrite(b, dendriteProcess{soma: s, noise: noise, angle: a, index: i, apical: s.hub && i == 0})
	}
	b.WriteString(`"/>`)
}

// dendriteProcess is one process of an arbor: where it leaves the soma and
// which noise indices shape it. The apical one is the pyramidal cell's long
// dendrite: it forks twice and grows a side branch halfway up.
type dendriteProcess struct {
	soma   somaSpec
	noise  func(int) float64
	angle  float64
	index  int
	apical bool
}

// dendrite appends one process: a trunk bowed by a seeded offset, forking
// into two branches.
func dendrite(b *strings.Builder, p dendriteProcess) {
	centreX, centreY, radius := float64(p.soma.x), float64(p.soma.y), float64(p.soma.r)
	trunk := radius * (dendriteTrunk + p.noise(p.index+10)*0.5)
	if p.apical {
		trunk = radius * dendriteApical
	}
	bow := (p.noise(p.index+20) - 0.5) * radius * 0.6
	start := polar(centreX, centreY, radius, p.angle)
	mid := polar(centreX, centreY, radius+trunk/2, p.angle)
	mid.x, mid.y = mid.x-bow*math.Sin(p.angle), mid.y+bow*math.Cos(p.angle)
	tip := polar(centreX, centreY, radius+trunk, p.angle)
	fmt.Fprintf(b, "M%.0f,%.0f Q%.0f,%.0f %.0f,%.0f", start.x, start.y, mid.x, mid.y, tip.x, tip.y)
	for _, side := range dendriteSides {
		branchAngle := p.angle + side*(dendriteFork+p.noise(p.index+30)*0.3)
		branchLen := radius * (dendriteBranch + p.noise(p.index+40)*0.4)
		branch := polar(tip.x, tip.y, branchLen, branchAngle)
		fmt.Fprintf(b, " M%.0f,%.0f L%.0f,%.0f", tip.x, tip.y, branch.x, branch.y)
		if !p.apical {
			continue
		}
		fork := polar(branch.x, branch.y, branchLen*0.7, branchAngle+side*0.5)
		fmt.Fprintf(b, " M%.0f,%.0f L%.0f,%.0f", branch.x, branch.y, fork.x, fork.y)
		half := polar(centreX, centreY, radius+trunk*0.55, p.angle)
		twig := polar(half.x, half.y, branchLen, p.angle+side*1.1)
		fmt.Fprintf(b, " M%.0f,%.0f L%.0f,%.0f", half.x, half.y, twig.x, twig.y)
	}
}

// nucleus draws the inner body that marks a soma as a cell rather than a dot.
func nucleus(b *strings.Builder, cx, cy, r int) {
	fmt.Fprintf(b, `<circle class="nucleus" cx="%d" cy="%d" r="%d"/>`, cx, cy, max(2, r*2/5))
}
