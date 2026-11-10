package web

import (
	"fmt"
	"math"
	"strings"
)

// The universe map draws each world as one region of a brain: a cortical lobe
// whose rim swells into gyri, with sulci bowing in from the folds. Seeded
// on the world, so a lobe keeps its shape across renders.

// cortexSpec places one lobe. class is the rim's: it carries soma, so hover,
// pulse and focus treat the lobe as any other node.
type cortexSpec struct {
	x, y, r int
	seed    string
	class   string
}

const (
	cortexGyrusPitch = 19.0 // rim length (px) per gyrus
	cortexMinGyri    = 5
	cortexMaxGyri    = 10
	cortexSwell      = 0.16 // tallest gyrus crown above its folds, as a share of r
	cortexFlatten    = 0.8  // a lobe is wider than tall
	cortexLopsided   = 0.12 // the low-frequency swell that makes a lobe, not a disc
	cortexTilt       = 0.4  // largest turn either way, radians
	cortexReach      = 0.7  // a long sulcus's depth, as a share of r
	cortexBend       = 0.32 // a sulcus's bow, as a share of its depth
)

// cortexFrame maps lobe-local points into the SVG: squashed, tilted, centred
// on the world.
type cortexFrame struct {
	cx, cy, cos, sin float64
}

func (f cortexFrame) at(p svgPoint) svgPoint {
	y := p.y * cortexFlatten
	return svgPoint{f.cx + p.x*f.cos - y*f.sin, f.cy + p.x*f.sin + y*f.cos}
}

// cortex draws the lobe straight into b: the rim, then its sulci.
func cortex(b *strings.Builder, s cortexSpec) {
	noise := seededNoise(s.seed)
	r := float64(s.r)
	// The crowns reach r at most, so edges and labels trim as for a circle.
	fold := r / ((1 + cortexSwell) * (1 + cortexLopsided))
	gyri := min(max(int(math.Round(2*math.Pi*r/cortexGyrusPitch)), cortexMinGyri), cortexMaxGyri)
	tilt := (noise(97)*2 - 1) * cortexTilt
	frame := cortexFrame{cx: float64(s.x), cy: float64(s.y), cos: math.Cos(tilt), sin: math.Sin(tilt)}

	// Folds unevenly spaced, so the gyri differ in width like a real cortex,
	// on an outline swollen one side and pinched the other.
	phase := noise(93) * 2 * math.Pi
	shape := func(a float64) float64 { return 1 + cortexLopsided*(0.6*math.Cos(a-phase)+0.4*math.Cos(2*a-phase)) }
	angles := make([]float64, gyri+1)
	folds := make([]svgPoint, gyri+1)
	for i := range gyri {
		angles[i] = 2 * math.Pi * (float64(i) + 0.5*(noise(i)-0.5)) / float64(gyri)
		folds[i] = polar(0, 0, fold*shape(angles[i])*(0.95+0.05*noise(i+40)), angles[i])
	}
	angles[gyri], folds[gyri] = angles[0]+2*math.Pi, folds[0]

	start := frame.at(folds[0])
	fmt.Fprintf(b, `<path class="%s" d="M%.1f,%.1f`, s.class, start.x, start.y)
	for i := range gyri {
		// A quadratic peaks halfway between its chord and its control point,
		// so the control sits past the crown by the chord's sag.
		mid := (angles[i] + angles[i+1]) / 2
		crown := fold * shape(mid) * (1 + cortexSwell*(0.6+0.4*noise(i+20)))
		sag := math.Hypot(folds[i].x+folds[i+1].x, folds[i].y+folds[i+1].y) / 2
		ctrl := frame.at(polar(0, 0, 2*crown-sag, mid))
		end := frame.at(folds[i+1])
		fmt.Fprintf(b, " Q%.1f,%.1f %.1f,%.1f", ctrl.x, ctrl.y, end.x, end.y)
	}
	b.WriteString(`Z"/><path class="sulci" d="`)
	// Every fold runs inward, long and short by turns, bowing either way so the
	// lobe reads as folded tissue, not a pinwheel; an odd count drops the last,
	// which would sit long beside the first.
	for i := range gyri - gyri%2 {
		reach := fold * shape(angles[i]) * (1 + cortexSwell) * cortexReach * (0.7 + 0.3*noise(i+80))
		if i%2 == 1 {
			reach *= 0.55
		}
		cortexSulcus(b, frame, cortexFold{
			fold: folds[i], angle: angles[i] + math.Pi + (noise(i+60)-0.5)*0.8, reach: reach,
			bend: reach * cortexBend * (noise(i+100)*2 - 1), branch: i%2 == 0 && noise(i+120) > 0.35,
		})
	}
	b.WriteString(`"/>`)
}

// cortexFold is one sulcus: where it leaves the rim, the way it runs, how far
// and how much it bows, and whether a branch forks off it.
type cortexFold struct {
	fold               svgPoint
	angle, reach, bend float64
	branch             bool
}

// cortexSulcus draws a fold as a bowed curve from the rim inward, with an
// optional branch curving off the opposite way from halfway in.
func cortexSulcus(b *strings.Builder, frame cortexFrame, f cortexFold) {
	in := polar(0, 0, 1, f.angle)
	side := svgPoint{-in.y, in.x}
	step := func(along, across float64) svgPoint {
		return frame.at(svgPoint{f.fold.x + in.x*along + side.x*across, f.fold.y + in.y*along + side.y*across})
	}
	curve := func(p0, ctrl, p1 svgPoint) {
		fmt.Fprintf(b, "M%.1f,%.1f Q%.1f,%.1f %.1f,%.1f ", p0.x, p0.y, ctrl.x, ctrl.y, p1.x, p1.y)
	}
	p0, ctrl, p1 := frame.at(f.fold), step(f.reach/2, f.bend), step(f.reach, 0)
	curve(p0, ctrl, p1)
	if f.branch {
		curve(quadraticAt(p0, ctrl, p1, 0.5), step(0.62*f.reach, -0.9*f.bend), step(0.72*f.reach, -1.6*f.bend))
	}
}
