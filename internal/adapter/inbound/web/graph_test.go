package web

import (
	"fmt"
	"net/http"
	"regexp"
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
		// Production derives backlinks and the neighborhood from one edge map;
		// the fake keeps them apart, so state both.
		neighbor: map[string]domain.Neighborhood{
			"/x.md": {Center: domain.Ref{World: "w.io", Path: "/x.md"},
				In: []domain.Ref{{World: "w.io", Path: "/referrer.md"}}},
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
	// The backlink pair carries its CSS anchor name through html/template's
	// CSS filter intact — a plain-string Anchor is rejected as ZgotmplZ
	// (custom idents need template.CSS), which silently unpins the card.
	if !regexp.MustCompile(`style="anchor-name:--pv-\d+"`).MatchString(body) {
		t.Errorf("backlink anchor-name missing or mangled: %s", body)
	}
	if strings.Contains(body, "ZgotmplZ") {
		t.Errorf("template rejected a CSS value (ZgotmplZ) in: %s", body)
	}
	// The backlink navigates onto the trail (truncate to focus, append).
	if !strings.Contains(body, `href="/t/w.io/d/x.md/~/w.io/d/referrer.md"`) {
		t.Errorf("backlink trail URL missing: %s", body)
	}
}

func TestGraphSVGCapsHighDegreeArcs(t *testing.T) {
	// A hub past graphMaxSide per side: the arc draws the cap, the overflow
	// is declared as a "+N more" note instead of overlapping labels.
	n := domain.Neighborhood{Center: domain.Ref{World: "soul", Path: "/hub.md"}}
	for i := range graphMaxSide + 7 {
		n.Out = append(n.Out, domain.Ref{World: "soul", Path: fmt.Sprintf("/out-%02d.md", i)})
	}
	for i := range graphMaxSide + 2 {
		n.In = append(n.In, domain.Ref{World: "soul", Path: fmt.Sprintf("/in-%02d.md", i)})
	}
	svg := string(graphSVG(n, func(r domain.Ref) string { return docRoute(r.World, r.Path) }, nil))

	if got := strings.Count(svg, "graph-out"); got != graphMaxSide {
		t.Errorf("outbound nodes drawn = %d, want cap %d", got, graphMaxSide)
	}
	if got := strings.Count(svg, `class="graph-node graph-in`); got != graphMaxSide {
		t.Errorf("inbound nodes drawn = %d, want cap %d", got, graphMaxSide)
	}
	if !strings.Contains(svg, "+7 more links") {
		t.Errorf("outbound overflow note missing: %s", svg)
	}
	if !strings.Contains(svg, "+2 more backlinks") {
		t.Errorf("inbound overflow note missing: %s", svg)
	}
}

func TestGraphSVGNoOverflowNoteAtCap(t *testing.T) {
	n := domain.Neighborhood{Center: domain.Ref{World: "soul", Path: "/c.md"}}
	for i := range graphMaxSide {
		n.Out = append(n.Out, domain.Ref{World: "soul", Path: fmt.Sprintf("/o-%02d.md", i)})
	}
	svg := string(graphSVG(n, func(r domain.Ref) string { return docRoute(r.World, r.Path) }, nil))
	if strings.Contains(svg, "graph-more") {
		t.Errorf("no overflow note expected at exactly the cap: %s", svg)
	}
}

// The overlay hotkeys were named only inside the overlays, so a reader had no
// way to learn them; the nav now carries both affordances with their keycaps.
func TestNavAdvertisesOverlayHotkeys(t *testing.T) {
	svc := &fakeReading{
		docs: map[string]domain.Document{"/x.md": {Title: "X", Path: "/x.md", HTML: "<p>x</p>"}},
		neighbor: map[string]domain.Neighborhood{
			"/x.md": {Center: domain.Ref{World: "w.io", Path: "/x.md"},
				Out: []domain.Ref{{World: "w.io", Path: "/y.md"}}},
		},
	}
	body := get(readingApp(t, svc), "/t/w.io/d/x.md").Body.String()
	for _, want := range []string{
		`class="nav-key graph-open" href="/w/w.io/g/x.md"`,
		`class="nav-key map-open" href="/w/w.io/u"`,
		`Graph <kbd>g</kbd>`,
		`Map <kbd>m</kbd>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("nav missing %q: %s", want, body)
		}
	}
}

// An unlinked doc has no graph: no overlay to summon, and neither the nav nor
// the margin offers a key that would open an empty canvas.
func TestNoGraphAffordanceWithoutReferences(t *testing.T) {
	svc := &fakeReading{
		docs: map[string]domain.Document{"/lonely.md": {Title: "Lonely", Path: "/lonely.md", HTML: "<p>x</p>"}},
	}
	body := get(readingApp(t, svc), "/t/w.io/d/lonely.md").Body.String()
	for _, unwanted := range []string{`id="graph-overlay"`, "nav-key graph-open", `class="graph-open"`} {
		if strings.Contains(body, unwanted) {
			t.Errorf("unlinked doc should offer no graph (%s): %s", unwanted, body)
		}
	}
	// The map is always viewable inside a world, and says so with its key.
	if !strings.Contains(body, `map <kbd>m</kbd>`) {
		t.Errorf("margin map affordance should name its key: %s", body)
	}
}

// The margin's graph affordance carries the reference count, so the reader
// knows there is something behind the key before pressing it.
func TestGraphAffordanceShowsDegree(t *testing.T) {
	svc := &fakeReading{
		docs: map[string]domain.Document{"/x.md": {Title: "X", Path: "/x.md", HTML: "<p>x</p>"}},
		neighbor: map[string]domain.Neighborhood{
			"/x.md": {Center: domain.Ref{World: "w.io", Path: "/x.md"},
				Out: []domain.Ref{{World: "w.io", Path: "/y.md"}, {World: "w.io", Path: "/z.md"}},
				In:  []domain.Ref{{World: "w.io", Path: "/r.md"}}},
		},
	}
	body := get(readingApp(t, svc), "/t/w.io/d/x.md").Body.String()
	for _, want := range []string{
		`graph <kbd>g</kbd> <span class="degree">3</span>`,
		`Graph <kbd>g</kbd> <span class="degree">3</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q: %s", want, body)
		}
	}
}

// The nav and the focused margin must point at the same graph. paneView settles
// the margin mid-loop, where a pane rendered later can still add the reference
// that turns the graph on; settleOverlays is the one place that knows.
func TestSettleOverlaysKeepsNavAndMarginInStep(t *testing.T) {
	svc := &fakeReading{
		docs: map[string]domain.Document{"/x.md": {Title: "X", Path: "/x.md", HTML: "<p>x</p>"}},
		neighbor: map[string]domain.Neighborhood{
			"/x.md": {Center: domain.Ref{World: "w.io", Path: "/x.md"},
				In: []domain.Ref{{World: "w.io", Path: "/late.md"}}},
		},
	}
	h := NewRoom(svc, "w.io", "/index.md").readingHandler()
	trail, err := parseTrail("w.io/d/x.md", "", "")
	if err != nil {
		t.Fatalf("parseTrail: %v", err)
	}

	// The margin arrives with no graph link, as it would when the reference was
	// recorded after this pane rendered.
	vm := canvasVM{Panes: []paneVM{{HasMargin: true, World: "w.io", Path: "/x.md"}}}
	h.settleOverlays(&vm, trail)

	if vm.OverlayGraphURL == "" {
		t.Fatal("nav should offer the graph once the reference is observed")
	}
	if got := vm.Panes[0].GraphURL; got != vm.OverlayGraphURL {
		t.Errorf("margin graph link = %q, nav = %q; they must not disagree", got, vm.OverlayGraphURL)
	}
	if vm.Panes[0].GraphDegree != 1 {
		t.Errorf("margin degree = %d, want 1", vm.Panes[0].GraphDegree)
	}

	// The reverse: a margin holding a stale link when the graph is gone.
	bare := &fakeReading{docs: map[string]domain.Document{"/x.md": {Title: "X", Path: "/x.md"}}}
	h = NewRoom(bare, "w.io", "/index.md").readingHandler()
	vm = canvasVM{Panes: []paneVM{{HasMargin: true, World: "w.io", Path: "/x.md",
		GraphURL: "/w/w.io/g/x.md"}}}
	h.settleOverlays(&vm, trail)
	if vm.Panes[0].GraphURL != "" || vm.OverlayGraphURL != "" {
		t.Errorf("no references should leave no affordance: margin=%q nav=%q",
			vm.Panes[0].GraphURL, vm.OverlayGraphURL)
	}
}

// A focus with no graph (the universe floor) advertises neither overlay.
func TestNavOmitsOverlayHotkeysOnFloor(t *testing.T) {
	body := get(readingApp(t, &fakeReading{}), "/t/u").Body.String()
	if strings.Contains(body, "nav-key graph-open") || strings.Contains(body, "nav-key map-open") {
		t.Errorf("floor should advertise no overlay hotkeys: %s", body)
	}
}
