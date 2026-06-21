package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

func TestGraphSVGRendersNeighborhood(t *testing.T) {
	n := domain.Neighborhood{
		Center: domain.Ref{World: "soul", Path: "/center.md"},
		Out:    []domain.Ref{{World: "soul", Path: "/out.md"}},
		In:     []domain.Ref{{World: "soul", Path: "/in.md"}},
	}
	svg := string(graphSVG(n, func(r domain.Ref) string { return docRoute(r.World, r.Path) }, nil))

	if !strings.Contains(svg, "<svg class=\"graph\"") {
		t.Errorf("not an svg: %s", svg)
	}
	if !strings.Contains(svg, "graph-center") || !strings.Contains(svg, "center") {
		t.Errorf("center node missing")
	}
	if !strings.Contains(svg, `href="/w/soul/d/out.md"`) || !strings.Contains(svg, "graph-out") {
		t.Errorf("outbound node missing: %s", svg)
	}
	if !strings.Contains(svg, `href="/w/soul/d/in.md"`) || !strings.Contains(svg, "graph-in") {
		t.Errorf("inbound node missing: %s", svg)
	}
}

func TestGraphSVGEmptyNeighborhood(t *testing.T) {
	n := domain.Neighborhood{Center: domain.Ref{World: "soul", Path: "/lonely.md"}}
	svg := string(graphSVG(n, func(_ domain.Ref) string { return "" }, nil))
	if !strings.Contains(svg, "graph-empty") {
		t.Errorf("empty neighborhood should render the honest empty state: %s", svg)
	}
}

func TestGraphPagePermalink(t *testing.T) {
	svc := &fakeReading{neighbor: map[string]domain.Neighborhood{
		"/x.md": {
			Center: domain.Ref{World: "soul", Path: "/x.md"},
			In:     []domain.Ref{{World: "soul", Path: "/y.md"}},
		},
	}}
	rec := get(readingApp(t, svc), "/w/soul/g/x.md")
	// A plain navigation to the graph permalink lands on the canvas (the graph as
	// a focused pane), not the standalone centered page — which would be a
	// one-way trap. The /w/ URL stays shareable; recipients follow this redirect.
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/t/soul/g/x.md" {
		t.Errorf("Location = %q, want /t/soul/g/x.md", loc)
	}
}

func TestTrailGraphPaneContinuesTrail(t *testing.T) {
	svc := &fakeReading{neighbor: map[string]domain.Neighborhood{
		"/x.md": {
			Center: domain.Ref{World: "w.io", Path: "/x.md"},
			Out:    []domain.Ref{{World: "w.io", Path: "/y.md"}},
		},
	}}
	// A doc pane then its graph pane, graph focused.
	rec := get(readingApp(t, svc), "/t/w.io/d/x.md/~/w.io/g/x.md")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	// Clicking a graph node continues the trail: truncate to the graph pane
	// (index 1) and append the node as a doc pane.
	if !strings.Contains(body, `href="/t/w.io/d/x.md/~/w.io/g/x.md/~/w.io/d/y.md"`) {
		t.Errorf("graph node does not continue the trail: %s", body)
	}
}

func TestGraphOverlayForFocusedDoc(t *testing.T) {
	svc := &fakeReading{
		docs: map[string]domain.Document{"/x.md": {Title: "X", Path: "/x.md", HTML: "<p>x</p>"}},
		neighbor: map[string]domain.Neighborhood{
			"/x.md": {Center: domain.Ref{World: "w.io", Path: "/x.md"},
				Out: []domain.Ref{{World: "w.io", Path: "/y.md"}}},
		},
	}
	body := get(readingApp(t, svc), "/t/w.io/d/x.md").Body.String()

	// The focused doc's graph overlay is embedded (summoned by `g`), not a pane.
	if !strings.Contains(body, `id="graph-overlay"`) {
		t.Errorf("graph overlay missing for focused doc: %s", body)
	}
	// A node click is a trail jump from the focus (navigating dismisses the overlay).
	if !strings.Contains(body, `href="/t/w.io/d/x.md/~/w.io/d/y.md"`) {
		t.Errorf("graph overlay node should jump the trail: %s", body)
	}
}

func TestGraphOverlayMarksWalkedNeighbors(t *testing.T) {
	// Trail y → x (focus x); x links to y, and y is on the trail, so y renders
	// as a walked node in x's overlay.
	svc := &fakeReading{
		docs: map[string]domain.Document{
			"/x.md": {Title: "X", Path: "/x.md", HTML: "<p>x</p>"},
			"/y.md": {Title: "Y", Path: "/y.md", HTML: "<p>y</p>"},
		},
		neighbor: map[string]domain.Neighborhood{
			"/x.md": {Center: domain.Ref{World: "w.io", Path: "/x.md"},
				Out: []domain.Ref{{World: "w.io", Path: "/y.md"}}},
		},
	}
	body := get(readingApp(t, svc), "/t/w.io/d/y.md/~/w.io/d/x.md").Body.String()
	if !strings.Contains(body, "graph-walked") {
		t.Errorf("neighbor on the trail should render as walked: %s", body)
	}
}

func TestDocMarginOffersGraphAndBacklinks(t *testing.T) {
	svc := &fakeReading{
		docs: map[string]domain.Document{
			"/x.md": {Title: "X", Path: "/x.md", HTML: "<p>x</p>", Status: "accepted"},
		},
		backlink: map[string][]domain.Ref{
			"/x.md": {{World: "w.io", Path: "/referrer.md"}},
		},
	}
	body := get(readingApp(t, svc), "/t/w.io/d/x.md").Body.String()

	// The margin's graph affordance opens the graph overlay (ADR 0006 §4): a /g/
	// permalink (degrade) that islands.js intercepts on the canvas.
	if !strings.Contains(body, `href="/w/w.io/g/x.md" class="graph-open"`) {
		t.Errorf("margin graph affordance missing: %s", body)
	}
	// The backlinks block lists the referrer with a hover-preview link.
	if !strings.Contains(body, "referenced by") ||
		!strings.Contains(body, `hx-get="/w/w.io/preview/referrer.md"`) {
		t.Errorf("backlinks block missing: %s", body)
	}
	// The backlink navigates onto the trail (truncate to focus, append).
	if !strings.Contains(body, `href="/t/w.io/d/x.md/~/w.io/d/referrer.md"`) {
		t.Errorf("backlink trail URL missing: %s", body)
	}
}
