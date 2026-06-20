package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

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
		`2 linked · 1 orphan`,
		// Linked doc node → the document pane, status-coded.
		`class="floor-doc status-wip"`,
		`href="/t/u/~/team-a/u//~/team-a/d/plans/a.md"`,
		// Reference edge drawn between two linked nodes.
		`class="graph-edge"`,
		// Orphan floated into the band with its own class + a band label.
		`world-map-orphan`,
		`href="/t/u/~/team-a/u//~/team-a/d/lonely.md"`,
		`>orphans</text>`,
		// The authenticated "new document" affordance.
		`class="world-map-new"`,
		`href="/w/team-a/new?dir=%2F"`,
	} {
		if !strings.Contains(svg, want) {
			t.Errorf("world-map svg missing %q\n---\n%s", want, svg)
		}
	}
	// Reference-only: no directory containment hubs or "+N more" aggregates.
	if strings.Contains(svg, "world-map-more") || strings.Contains(svg, `class="floor-world"`) {
		t.Errorf("reference-only map must not render dir hubs/aggregates:\n%s", svg)
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
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	// Permalink nodes use /w/ document routes, not trail URLs.
	if !strings.Contains(rec.Body.String(), `href="/w/team-a/d/plans/a.md"`) {
		t.Errorf("permalink world map should link to /w/ doc routes:\n%s", rec.Body.String())
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
