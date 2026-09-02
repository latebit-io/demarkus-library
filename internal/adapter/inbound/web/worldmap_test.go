package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

func TestTrailMapOverlayShell(t *testing.T) {
	svc := &fakeReading{docs: map[string]domain.Document{"/x.md": {Title: "X", Path: "/x.md", HTML: "<p>x</p>"}}}
	body := get(readingApp(t, svc), "/t/w.io/d/x.md").Body.String()
	// The map overlay shell is embedded (lazy htmx target) when the focus is in a world.
	if !strings.Contains(body, `id="map-overlay"`) || !strings.Contains(body, `data-map-url="/w/w.io/u?overlay=1"`) {
		t.Errorf("map overlay shell missing: %s", body)
	}
	// The margin "map" affordance opens it; href is the /u permalink (degrade).
	if !strings.Contains(body, `href="/w/w.io/u" class="map-open"`) {
		t.Errorf("margin map affordance missing: %s", body)
	}
}

func TestWorldMapOverlayFragment(t *testing.T) {
	svc := &fakeReading{worldMap: testWorldMap()}
	req := httptest.NewRequest(http.MethodGet, "/w/team-a/u?overlay=1", http.NoBody)
	req.Header.Set("HX-Current-URL", "http://x/t/team-a/d/index.md")
	rec := httptest.NewRecorder()
	readingApp(t, svc).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `class="floor world-map"`) {
		t.Errorf("overlay fragment should be the map SVG: %s", body)
	}
	// Overlay nodes extend the reader's trail (from HX-Current-URL), not /w/ permalinks.
	if !strings.Contains(body, `href="/t/team-a/d/index.md/~/team-a/d/plans/a.md"`) {
		t.Errorf("overlay node should extend the trail: %s", body)
	}
}

func testWorldMap() domain.WorldMap {
	return domain.WorldMap{
		World: domain.WorldInfo{Name: "team-a", URL: "mark://team-a"},
		Clusters: []domain.WorldCluster{
			{Dir: "", ListPath: "/", Docs: []domain.FloorDoc{
				{Path: "/index.md", Title: "Home", Importance: 0.9, Status: "accepted"},
			}},
			{Dir: "plans", ListPath: "/plans/", More: 4, Docs: []domain.FloorDoc{
				{Path: "/plans/a.md", Title: "Plan A", Importance: 0.8, Status: "wip"},
			}},
		},
		Edges: []domain.Edge{
			{From: domain.Ref{World: "team-a", Path: "/index.md"}, To: domain.Ref{World: "team-a", Path: "/plans/a.md"}},
		},
	}
}

func TestWorldMapSVGReferenceLayout(t *testing.T) {
	wm := testWorldMap()
	// index + plans/a are linked (a reference edge joins them); add an orphan to
	// exercise the floated band + caption.
	wm.Clusters[0].Docs = append(wm.Clusters[0].Docs,
		domain.FloorDoc{Path: "/lonely.md", Title: "Lonely", Status: "draft", Orphan: true})
	tr := trail{Panes: []paneAddr{{Kind: paneFloor}, {Kind: paneFloor, World: "team-a"}}, Focus: 1}
	docURL := func(p string) string {
		return trailURL(trailAfterClick(tr, 1, paneAddr{Kind: paneDoc, World: "team-a", Value: p}))
	}
	svg := string(worldMapSVG(wm, docURL, "/w/team-a/new?dir=%2F"))

	for _, want := range []string{
		`class="floor world-map"`,
		`class="world-map-caption"`,
		`2 connected · 1 unlinked`,
		// Linked doc node → the document pane, status-coded.
		`class="floor-doc status-wip"`,
		`href="/t/u/~/team-a/u//~/team-a/d/plans/a.md"`,
		// Reference edge drawn between two linked nodes; both sit on the one
		// ring, so it is spine-tier at rest.
		`class="graph-edge edge-spine"`,
		// Edgeless doc drawn dashed, still a trail link.
		`world-map-orphan`,
		`href="/t/u/~/team-a/u//~/team-a/d/lonely.md"`,
		// A one-doc directory folds into its parent: no group, no anchor.
		`href="/t/u/~/team-a/u//~/team-a/d/plans/a.md"`,
		// The authenticated "new document" affordance.
		`class="world-map-new"`,
		`href="/w/team-a/new?dir=%2F"`,
	} {
		if !strings.Contains(svg, want) {
			t.Errorf("world-map svg missing %q\n---\n%s", want, svg)
		}
	}
	// Small world: every doc is its own node, no aggregates at all.
	if strings.Contains(svg, "floor-agg") || strings.Contains(svg, `class="floor-world"`) {
		t.Errorf("a world within budget must not render aggregates:\n%s", svg)
	}
}

func TestWorldMapSVGEmptyCatalog(t *testing.T) {
	id := func(p string) string { return p }
	// Anonymous: empty message, no create link.
	svg := string(worldMapSVG(domain.WorldMap{World: domain.WorldInfo{Name: "w"}}, id, ""))
	if !strings.Contains(svg, "catalog is empty") || strings.Contains(svg, "Create the first") {
		t.Errorf("empty world map (anon) wrong: %s", svg)
	}
	// Authenticated: the empty world is the one place to create the first doc.
	svg = string(worldMapSVG(domain.WorldMap{World: domain.WorldInfo{Name: "w"}}, id, "/w/w/new?dir=%2F"))
	if !strings.Contains(svg, "Create the first document") || !strings.Contains(svg, `href="/w/w/new?dir=%2F"`) {
		t.Errorf("empty world map (authed) should offer create: %s", svg)
	}
}

func TestWorldMapSVGUnreadable(t *testing.T) {
	id := func(p string) string { return p }
	// Unreadable ≠ empty: a notice, and no "create" link even when authed (we
	// don't know the catalog is empty).
	svg := string(worldMapSVG(domain.WorldMap{World: domain.WorldInfo{Name: "w"}, Unreadable: true}, id, "/w/w/new?dir=%2F"))
	if !strings.Contains(svg, "could not be read") {
		t.Errorf("unreadable world map should say so: %s", svg)
	}
	if strings.Contains(svg, "Create the first") || strings.Contains(svg, "world-map-new") {
		t.Errorf("unreadable world map must not offer create: %s", svg)
	}
}

func TestWorldMapSVGEscapesContent(t *testing.T) {
	wm := domain.WorldMap{World: domain.WorldInfo{Name: "w"}, Clusters: []domain.WorldCluster{
		{Dir: "x", ListPath: "/x/", Docs: []domain.FloorDoc{
			{Path: "/x/e.md", Title: `<script>"x"</script>`, Importance: 0.5, Status: "draft"}}},
	}}
	svg := string(worldMapSVG(wm, func(p string) string { return p }, ""))
	if strings.Contains(svg, "<script>") {
		t.Errorf("unescaped title in svg: %s", svg)
	}
}

func TestTrailWorldMapPaneFocusedLive(t *testing.T) {
	svc := &fakeReading{worldMap: testWorldMap()}
	// Floor (unfocused) → cached; world map (focused) → live.
	rec := get(readingApp(t, svc), "/t/u/~/team-a/u/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	want := "FloorCached,WorldMap"
	if got := strings.Join(svc.calls, ","); got != want {
		t.Errorf("calls = %q, want %q", got, want)
	}
	if !strings.Contains(rec.Body.String(), `class="floor world-map"`) {
		t.Errorf("world-map svg missing from body pane")
	}

	// Unfocused world map → cached.
	svc2 := &fakeReading{worldMap: testWorldMap(), doc: domain.Document{Title: "D", Path: "/x.md", HTML: "<p>x</p>"}}
	get(readingApp(t, svc2), "/t/team-a/u//~/team-a/d/x.md")
	if got := strings.Join(svc2.calls, ","); got != "WorldMapCached,Read /x.md" {
		t.Errorf("calls = %q, want WorldMapCached,Read /x.md", got)
	}
}

func TestWorldMapPagePermalink(t *testing.T) {
	svc := &fakeReading{worldMap: testWorldMap()}
	rec := get(readingApp(t, svc), "/w/team-a/u")
	// A plain navigation to the map permalink lands on the canvas (the map as a
	// pane), not the standalone centered page. The /w/ URL stays shareable
	// (recipients follow this redirect); the overlay pull-up (?overlay=1) and
	// no-JS both still resolve to a usable view.
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/t/team-a/u/" {
		t.Errorf("Location = %q, want /t/team-a/u/", loc)
	}
}

func TestTrailWorldMapErrorHandling(t *testing.T) {
	svc := &fakeReading{worldMapErr: domain.ErrUnauthorized}
	if rec := get(readingApp(t, svc), "/t/team-a/u/"); rec.Code != http.StatusUnauthorized {
		t.Errorf("focused world-map error -> %d, want 401", rec.Code)
	}
	// Unfocused world-map error tombstones, the rest of the trail survives.
	svc2 := &fakeReading{worldMapErr: domain.ErrNotFound, doc: domain.Document{Title: "D", Path: "/x.md"}}
	rec := get(readingApp(t, svc2), "/t/team-a/u//~/w.io/d/x.md")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `class="pane spine gone"`) {
		t.Errorf("unfocused world-map error must tombstone: %d", rec.Code)
	}
}

// A rel-typed edge draws dashed (edge-rel) with its predicate as the tooltip;
// plain edges stay untouched.
func TestWorldMapSVGRelEdge(t *testing.T) {
	wm := testWorldMap()
	wm.Edges[0].Rel = "supersedes"
	svg := string(worldMapSVG(wm, func(p string) string { return p }, ""))
	for _, want := range []string{
		`class="graph-edge edge-spine edge-rel"`,
		`<title>supersedes</title>`,
	} {
		if !strings.Contains(svg, want) {
			t.Errorf("world-map svg missing %q\n---\n%s", want, svg)
		}
	}
}

// wmHubFixture builds a world of hubs A and B joined by one edge, mids linked
// from both (degree 2, high importance) and leaves linked from A only.
func wmHubFixture(mids, leaves int) domain.WorldMap {
	ref := func(p string) domain.Ref { return domain.Ref{World: "w", Path: p} }
	wm := domain.WorldMap{World: domain.WorldInfo{Name: "w"}}
	cl := domain.WorldCluster{Dir: "", ListPath: "/"}
	cl.Docs = append(cl.Docs, domain.FloorDoc{Path: "/a.md", Title: "A"}, domain.FloorDoc{Path: "/b.md", Title: "B"})
	wm.Edges = append(wm.Edges, domain.Edge{From: ref("/a.md"), To: ref("/b.md")})
	for i := 1; i <= mids; i++ {
		p := fmt.Sprintf("/m%02d.md", i)
		cl.Docs = append(cl.Docs, domain.FloorDoc{Path: p, Title: "Mid", Importance: 0.9})
		wm.Edges = append(wm.Edges, domain.Edge{From: ref("/a.md"), To: ref(p)}, domain.Edge{From: ref("/b.md"), To: ref(p)})
	}
	for i := 1; i <= leaves; i++ {
		p := fmt.Sprintf("/l%03d.md", i)
		cl.Docs = append(cl.Docs, domain.FloorDoc{Path: p, Title: "Leaf"})
		wm.Edges = append(wm.Edges, domain.Edge{From: ref("/a.md"), To: ref(p)})
	}
	wm.Clusters = []domain.WorldCluster{cl}
	return wm
}

// wmEdgeClass returns the class attribute of the drawn edge from→to, "" if absent.
func wmEdgeClass(svg, from, to string) string {
	i := strings.Index(svg, `data-from="`+from+`" data-to="`+to+`"`)
	if i < 0 {
		return ""
	}
	start := strings.LastIndex(svg[:i], `class="`) + len(`class="`)
	return svg[start : start+strings.Index(svg[start:], `"`)]
}

// Rest-state tiers: edges among the 13 top-ranked nodes are the spine, BFS
// spokes are tree, the rest dim; labels past the top 13 are zoom/hover-only.
// Rank order is A, B, the thirty mids (degree 2), then the leaves, so the hub
// tier is A, B and eleven mids.
func TestWorldMapSVGRestStateTiers(t *testing.T) {
	ref := func(p string) domain.Ref { return domain.Ref{World: "w", Path: p} }
	wm := wmHubFixture(30, 20)
	// Cross-references reached from A first, so not tree edges: between two
	// outer-ring leaves, and from hub B to a ring-1 leaf.
	wm.Edges = append(wm.Edges, domain.Edge{From: ref("/l019.md"), To: ref("/l020.md")},
		domain.Edge{From: ref("/b.md"), To: ref("/l005.md")})

	svg := string(worldMapRender(wm, func(p string) string { return p }, "", wmOpts{chunk: 1000}))
	for _, c := range []struct{ from, to, want string }{
		{"/a.md", "/b.md", "graph-edge edge-spine"},
		{"/b.md", "/m01.md", "graph-edge edge-spine"}, // not a tree edge, still hub tier
		{"/a.md", "/m20.md", "graph-edge edge-tree"},
		{"/a.md", "/l015.md", "graph-edge edge-tree"},
		{"/b.md", "/l005.md", "graph-edge edge-dim"},
		{"/l019.md", "/l020.md", "graph-edge edge-dim"},
	} {
		if got := wmEdgeClass(svg, c.from, c.to); got != c.want {
			t.Errorf("%s→%s class = %q, want %q", c.from, c.to, got, c.want)
		}
	}
	// The spine is exactly the 23 edges among the 13 hub-tier nodes.
	if n := strings.Count(svg, "edge-spine"); n != 23 {
		t.Errorf("spine edge count = %d, want 23", n)
	}
	// 52 linked nodes: the top 13 labeled at rest, the other 39 label-lod.
	if n := strings.Count(svg, `class="floor-doc-label label-lod"`); n != 39 {
		t.Errorf("label-lod count = %d, want 39", n)
	}
}

// The spiral grows with √n and every node lands inside the viewBox: no cap
// piles the tail onto one ring, and a bigger catalog gets a bigger canvas.
func TestWorldMapSVGSpiralGrows(t *testing.T) {
	circle := regexp.MustCompile(`<circle [^>]*cx="(-?\d+)" cy="(-?\d+)"`)
	box := func(n int) (w, h int) {
		svg := string(worldMapRender(wmHubFixture(0, n), func(p string) string { return p }, "", wmOpts{chunk: 1000}))
		vb := regexp.MustCompile(`viewBox="0 0 (\d+) (\d+)"`).FindStringSubmatch(svg)
		if vb == nil {
			t.Fatalf("n=%d: no viewBox in %s", n, svg[:120])
		}
		w, _ = strconv.Atoi(vb[1])
		h, _ = strconv.Atoi(vb[2])
		for _, m := range circle.FindAllStringSubmatch(svg, -1) {
			x, _ := strconv.Atoi(m[1])
			y, _ := strconv.Atoi(m[2])
			if x < 0 || y < 0 || x > w || y > h {
				t.Errorf("n=%d: node at (%d,%d) outside %dx%d", n, x, y, w, h)
			}
		}
		return w, h
	}
	w1, h1 := box(50)
	w2, h2 := box(200)
	if w2 <= w1 || h2 <= h1 {
		t.Errorf("200 nodes (%dx%d) should need a larger box than 50 (%dx%d)", w2, h2, w1, h1)
	}
}

// wmDirFixture builds a world shaped like a knowledge base: root docs plus
// directories of the given sizes, every doc linked from the root index.
func wmDirFixture(dirs map[string]int) domain.WorldMap {
	ref := func(p string) domain.Ref { return domain.Ref{World: "w", Path: p} }
	wm := domain.WorldMap{World: domain.WorldInfo{Name: "w"}}
	cl := domain.WorldCluster{Dir: "", ListPath: "/"}
	cl.Docs = append(cl.Docs, domain.FloorDoc{Path: "/index.md", Title: "Home", Importance: 0.9})
	names := make([]string, 0, len(dirs))
	for d := range dirs {
		names = append(names, d)
	}
	sort.Strings(names)
	for _, d := range names {
		for i := 1; i <= dirs[d]; i++ {
			p := fmt.Sprintf("/%s/d%03d.md", d, i)
			cl.Docs = append(cl.Docs, domain.FloorDoc{Path: p, Title: d + " " + strconv.Itoa(i)})
			wm.Edges = append(wm.Edges, domain.Edge{From: ref("/index.md"), To: ref(p)})
		}
	}
	wm.Clusters = []domain.WorldCluster{cl}
	return wm
}

// Rest state auto-expands the smallest directories within the budget and
// leaves the big one collapsed as an aggregate that carries its rolled-up
// edges. Aggregates link to their listing; with an openURL they also carry
// the htmx expand swap.
func TestWorldMapAggregationRestState(t *testing.T) {
	wm := wmDirFixture(map[string]int{"genres": 200, "artists": 30, "works": 10})
	openURL := func(keys []string) string { return "/w/w/u?overlay=1&open=" + strings.Join(keys, ",") }
	svg := string(worldMapRender(wm, func(p string) string { return "/d" + p }, "", wmOpts{openURL: openURL}))
	for _, want := range []string{
		`data-node="g:genres"`, `>genres (200)<`, `href="/d/genres/"`,
		`hx-get="/w/w/u?overlay=1&amp;open=genres" hx-target="#map-canvas"`,
		// Root index → genres rolls 200 document edges into one bundle.
		`data-from="/index.md" data-to="g:genres"`, `edge-bundle`, `style="stroke-width:4.0"`,
		// artists (30) and works (10) fit the budget: expanded, with anchors.
		`data-node="g:artists"`, `floor-agg-anchor`, `data-node="/artists/d001.md"`, `data-node="/works/d010.md"`,
		`hx-get="/w/w/u?overlay=1&amp;open=-artists"`,
		`241 connected · 0 unlinked · 41 of 241 shown`,
	} {
		if !strings.Contains(svg, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(svg, `data-node="/genres/d001.md"`) {
		t.Errorf("collapsed genres must not draw members")
	}
}

// Opening a chunked directory shows its first page by rank plus a "more"
// aggregate whose swap asks for the next page; closing an auto-expanded one
// collapses it.
func TestWorldMapAggregationOpenAndPage(t *testing.T) {
	wm := wmDirFixture(map[string]int{"genres": 100, "works": 5})
	openURL := func(keys []string) string { return "?open=" + strings.Join(keys, ",") }
	render := func(open ...string) string {
		return string(worldMapRender(wm, func(p string) string { return p }, "", wmOpts{open: open, openURL: openURL}))
	}
	svg := render("genres", "-works")
	for _, want := range []string{
		`data-node="/genres/d001.md"`, `data-node="/genres/d040.md"`,
		`data-node="m:genres"`, `>60 more<`, `hx-get="?open=-works,genres@2"`,
		`data-node="g:works"`, `>works (5)<`,
		`hx-get="?open=-genres,-works"`, // the anchor collapses genres again
	} {
		if !strings.Contains(svg, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(svg, `data-node="/genres/d041.md"`) || strings.Contains(svg, `data-node="/works/d001.md"`) {
		t.Errorf("page 1 must stop at the chunk and works must stay collapsed")
	}
	svg = render("genres@2")
	if !strings.Contains(svg, `data-node="/genres/d080.md"`) || strings.Contains(svg, `data-node="/genres/d081.md"`) {
		t.Errorf("page 2 should show 80 members")
	}
}

// The overlay handler threads the open param into the fragment and builds
// swap URLs against the same route.
func TestWorldMapOverlayOpenParam(t *testing.T) {
	svc := &fakeReading{worldMap: wmDirFixture(map[string]int{"genres": 100})}
	req := httptest.NewRequest(http.MethodGet, "/w/w/u?overlay=1&open=genres", http.NoBody)
	req.Header.Set("HX-Current-URL", "http://x/t/w/d/index.md")
	rec := httptest.NewRecorder()
	readingApp(t, svc).ServeHTTP(rec, req)
	body := rec.Body.String()
	for _, want := range []string{
		`data-node="/genres/d001.md"`,
		`hx-get="/w/w/u?overlay=1&amp;open=genres%402" hx-target="#map-canvas"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in %s", want, body[:300])
		}
	}
}

// Small leaf groups ring their anchor with every label shown; a member that
// links to most of its siblings is the hub and holds the centre.
func TestWorldMapAggregationRingAndHub(t *testing.T) {
	ref := func(p string) domain.Ref { return domain.Ref{World: "w", Path: p} }
	wm := wmDirFixture(map[string]int{"adr": 8, "big": 60})
	for i := 2; i <= 8; i++ {
		wm.Edges = append(wm.Edges, domain.Edge{From: ref("/adr/d001.md"), To: ref(fmt.Sprintf("/adr/d%03d.md", i))})
	}
	svg := string(worldMapRender(wm, func(p string) string { return p }, "", wmOpts{}))
	circle := func(id string) (x, y int) {
		i := strings.Index(svg, `data-node="`+id+`"`)
		if i < 0 {
			t.Fatalf("no node %s", id)
		}
		m := regexp.MustCompile(`cx="(-?\d+)" cy="(-?\d+)"`).FindStringSubmatch(svg[i:])
		x, _ = strconv.Atoi(m[1])
		y, _ = strconv.Atoi(m[2])
		return x, y
	}
	hx, hy := circle("/adr/d001.md")
	ax, ay := circle("g:adr")
	if hx != ax || ay >= hy {
		t.Errorf("hub should hold the centre with the anchor above it: hub (%d,%d) anchor (%d,%d)", hx, hy, ax, ay)
	}
	// Ring members keep their labels at rest.
	for i := 2; i <= 8; i++ {
		id := fmt.Sprintf("/adr/d%03d.md", i)
		j := strings.Index(svg, `data-node="`+id+`"`)
		if strings.Contains(svg[j:j+400], "label-lod") {
			t.Errorf("ring member %s should be labeled at rest", id)
		}
	}
	// The ring members share the anchor's centre at a fixed vertical radius.
	_, y2 := circle("/adr/d002.md")
	if d := hy - y2; d != int(wmRingRadius(7)) {
		t.Errorf("ring radius = %d, want %d", d, int(wmRingRadius(7)))
	}
}
