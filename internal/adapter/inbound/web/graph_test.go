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
	svg := string(graphSVG(n, func(r domain.Ref) string { return docRoute(r.World, r.Path) }))

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
	svg := string(graphSVG(n, func(_ domain.Ref) string { return "" }))
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
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	// Nodes on the permalink link to /w/ document permalinks.
	if !strings.Contains(body, `href="/w/soul/d/y.md"`) {
		t.Errorf("permalink graph node not a /w/ route: %s", body)
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

	// The margin's graph affordance opens this doc's graph pane on the trail.
	if !strings.Contains(body, `href="/t/w.io/d/x.md/~/w.io/g/x.md"`) {
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
