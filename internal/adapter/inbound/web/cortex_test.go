package web

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var cortexNumber = regexp.MustCompile(`-?\d+(?:\.\d+)?`)

// cortexPaths draws one lobe and splits it into the rim's and the sulci's
// path data.
func cortexPaths(t *testing.T, s cortexSpec) (rim, sulci string) {
	t.Helper()
	var b strings.Builder
	cortex(&b, s)
	m := regexp.MustCompile(`<path class="[^"]*" d="([^"]*)"/><path class="sulci" d="([^"]*)"/>`).FindStringSubmatch(b.String())
	if m == nil {
		t.Fatalf("cortex markup not a rim then sulci: %s", b.String())
	}
	return m[1], m[2]
}

func cortexCoords(t *testing.T, d string) []float64 {
	t.Helper()
	numbers := cortexNumber.FindAllString(d, -1)
	out := make([]float64, 0, len(numbers))
	for _, n := range numbers {
		v, err := strconv.ParseFloat(n, 64)
		if err != nil {
			t.Fatalf("parse %q: %v", n, err)
		}
		out = append(out, v)
	}
	return out
}

// Edges and labels trim a world as a circle of radius r, so the rim's curve
// and every sulcus must stay inside it.
func TestCortexStaysInsideItsRadius(t *testing.T) {
	t.Parallel()
	for _, seed := range []string{"latebit", "soul.demarkus.io", "team-a", "a", ""} {
		for _, r := range []int{12, 20, 34, 52, 80} {
			const cx, cy = 200, 150
			rim, sulci := cortexPaths(t, cortexSpec{x: cx, y: cy, r: r, seed: seed, class: "c"})
			inside := func(x, y float64) bool { return math.Hypot(x-cx, y-cy) <= float64(r)+0.2 }
			// The rim is M then quadratics: (control, end) pairs.
			pts := cortexCoords(t, rim)
			if len(pts) < 2+4*cortexMinGyri {
				t.Fatalf("%q r=%d: rim has %d numbers", seed, r, len(pts))
			}
			from := svgPoint{pts[0], pts[1]}
			for i := 2; i+3 < len(pts); i += 4 {
				ctrl, end := svgPoint{pts[i], pts[i+1]}, svgPoint{pts[i+2], pts[i+3]}
				for step := 0.0; step <= 1; step += 0.05 {
					if p := quadraticAt(from, ctrl, end, step); !inside(p.x, p.y) {
						t.Fatalf("%q r=%d: rim reaches (%.1f,%.1f), outside the radius", seed, r, p.x, p.y)
					}
				}
				from = end
			}
			// Sulci are quadratics too; their hull (ends and controls) bounds them.
			coords := cortexCoords(t, sulci)
			if len(coords) == 0 {
				t.Errorf("%q r=%d: no sulci drawn", seed, r)
			}
			for i := 0; i+1 < len(coords); i += 2 {
				if !inside(coords[i], coords[i+1]) {
					t.Errorf("%q r=%d: sulcus point (%.1f,%.1f) outside the radius", seed, r, coords[i], coords[i+1])
				}
			}
		}
	}
}

// A lobe is a function of its world: the same seed redraws the same shape,
// another seed another one.
func TestCortexIsSeededByWorld(t *testing.T) {
	t.Parallel()
	spec := cortexSpec{x: 100, y: 100, r: 40, seed: "latebit", class: "c"}
	first, _ := cortexPaths(t, spec)
	again, _ := cortexPaths(t, spec)
	if first != again {
		t.Errorf("same world drew two shapes")
	}
	spec.seed = "research"
	if other, _ := cortexPaths(t, spec); other == first {
		t.Errorf("two worlds drew the same shape")
	}
}
