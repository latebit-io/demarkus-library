package web

import (
	"net/http"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

func testFloor() domain.Floor {
	return domain.Floor{Worlds: []domain.FloorWorld{
		{World: domain.WorldInfo{Name: "team-a", URL: "mark://team-a.example.org"},
			Docs: []domain.FloorDoc{
				{Path: "/index.md", Title: "Hub", Importance: 0.95, Status: "accepted"},
				{Path: "/adr/0005.md", Title: "ADR 0005 — a very long title that needs trimming", Importance: 0.9, Status: "draft"},
			}},
		{World: domain.WorldInfo{Name: "old-world"}, Err: true},
	}}
}

func TestFloorChunkRoundTrip(t *testing.T) {
	tr, err := parseTrail("u", "", "")
	if err != nil {
		t.Fatalf("parseTrail(u): %v", err)
	}
	if len(tr.Panes) != 1 || tr.Panes[0].Kind != paneFloor {
		t.Fatalf("parsed %+v", tr)
	}
	if got := trailURL(tr); got != "/t/u" {
		t.Errorf("trailURL = %q", got)
	}
	// Floor + doc trail round-trips too.
	tr2, err := parseTrail("u/~/w.io/d/x.md", "0", "")
	if err != nil {
		t.Fatalf("parseTrail: %v", err)
	}
	if len(tr2.Panes) != 2 || tr2.Panes[0].Kind != paneFloor || tr2.Focus != 0 {
		t.Fatalf("parsed %+v", tr2)
	}
	if got := trailURL(tr2); got != "/t/u/~/w.io/d/x.md?focus=0" {
		t.Errorf("trailURL = %q", got)
	}
}

// atlasFloor is a universe with one catalogued world that links out, a portal
// it links to, and an unreadable world.
func atlasFloor() domain.Floor {
	return domain.Floor{
		Worlds: []domain.FloorWorld{
			{World: domain.WorldInfo{Name: "team-a"}, Truncated: true, Docs: []domain.FloorDoc{
				{Path: "/index.md", Title: "Team Hub", Importance: 0.95, Status: "accepted"},
				{Path: "/.well-known/library/branding.md", Title: "Branding", Importance: 0.9},
				{Path: "/adr/0001.md", Title: "ADR 1", Importance: 0.85, Status: "accepted"},
				{Path: "/plans/a.md", Title: "Plan A", Importance: 0.8, Status: "draft"},
				{Path: "/plans/b.md", Title: "Plan B", Importance: 0.7, Status: "draft"},
				{Path: "/notes.md", Title: "Notes", Importance: 0.6, Status: "draft"},
				{Path: "/zeta/z.md", Title: "Zeta", Importance: 0.1, Status: "draft"},
			}},
			{World: domain.WorldInfo{Name: "old-world"}, Err: true},
			{World: domain.WorldInfo{Name: "remote.example.org"}, Portal: true},
		},
		Edges: []domain.Edge{
			{From: domain.Ref{World: "team-a", Path: "/index.md"}, To: domain.Ref{World: "remote.example.org", Path: "/"}, Count: 2},
			{From: domain.Ref{World: "team-a", Path: "/index.md"}, To: domain.Ref{World: "team-a", Path: "/notes.md"}},
		},
	}
}

// Each world is a door with its catalog at a glance: counts, sections
// largest first, and featured documents, all trail-aware and none of the
// world's machinery (dot directories).
func TestWorldCards(t *testing.T) {
	t.Parallel()

	tr := trail{Panes: []paneAddr{{Kind: paneFloor}}, Focus: 0}
	cards := worldCards(atlasFloor(), tr, 0)
	if len(cards) != 3 {
		t.Fatalf("cards = %d, want 3", len(cards))
	}
	exact := worldCards(testFloor(), tr, 0)
	labels := func(links []cardLink) []string {
		out := make([]string, 0, len(links))
		for _, l := range links {
			out = append(out, l.Label+" "+l.URL)
		}
		return out
	}
	tests := []struct {
		name string
		got  any
		want any
	}{
		{name: "door enters the stacks", got: cards[0].URL, want: "/t/u/~/team-a/d/"},
		{name: "title from the root index", got: cards[0].Title, want: "Team Hub"},
		{name: "sampled stats read at least", got: cards[0].Stats, want: "6+ docs · 2+ accepted · 3+ sections · links to 1 world"},
		{name: "full catalog stats are exact", got: exact[0].Stats, want: "2 docs · 1 accepted · 1 section"},
		{name: "sections largest first, then by name", got: labels(cards[0].Sections), want: []string{
			"plans/ /t/u/~/team-a/d/plans/", "adr/ /t/u/~/team-a/d/adr/", "zeta/ /t/u/~/team-a/d/zeta/"}},
		{name: "featured skips the root index", got: labels(cards[0].Featured), want: []string{
			"ADR 1 /t/u/~/team-a/d/adr/0001.md", "Plan A /t/u/~/team-a/d/plans/a.md", "Plan B /t/u/~/team-a/d/plans/b.md"}},
		{name: "constellation drawn", got: strings.Count(string(cards[0].Sky), "<circle"), want: 6},
		{name: "unreadable world has no catalog", got: cards[1].Stats + string(cards[1].Sky), want: ""},
		{name: "portal counts its inbound links", got: cards[2].Stats, want: "linked from 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if !reflect.DeepEqual(tt.got, tt.want) {
				t.Errorf("got %v, want %v", tt.got, tt.want)
			}
		})
	}
}

func TestFloorSummary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		floor domain.Floor
		want  string
	}{
		{name: "sampled world reads at least", floor: atlasFloor(), want: "2 worlds · 6+ docs · 1 unreadable · 1 portal"},
		{name: "unreadable world is named beside the total", floor: testFloor(), want: "2 worlds · 2 docs · 1 unreadable"},
		{name: "exact when every world reads", floor: domain.Floor{Worlds: testFloor().Worlds[:1]}, want: "1 world · 2 docs"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := floorSummary(tt.floor); got != tt.want {
				t.Errorf("floorSummary = %q, want %q", got, tt.want)
			}
		})
	}
}

// A card lays the world's own branding over its catalog: the declared name,
// logo, and accent win, and the address stays visible beneath the name.
// Unreadable and portal worlds keep their bare names.
func TestFloorPaneRendersBrandedCards(t *testing.T) {
	svc := inWorldSvc("# Logo\n\nThe mark.\n\n```svg\n<svg xmlns='http://www.w3.org/2000/svg'/>\n```\n")
	svc.raws[WorldBrandDoc] = domain.RawDocument{Body: "# Branding\n\nIdentity.\n\n```yaml\nname: Team Room\ntheme:\n  accent: \"#8250df\"\n```\n"}
	svc.rawsWorld = "team-a"
	svc.floor = atlasFloor()
	app, _ := inWorldApp(t, svc)

	rec := get(app, "/t/u")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<h1 class="sr-only">Universe</h1>`, // the pane head names it; readers still get a heading
		`<p class="floor-bar"><span class="floor-sum">2 worlds · 6&#43; docs · 1 unreadable · 1 portal</span>`, // html/template escapes "+"
		`<li class="world-card" style="--world-accent: #8250df">`,
		`<a class="world-door" href="/t/u/~/team-a/d/"><img class="world-logo" src="/theme/worlds/team-a/logo" alt=""><span class="world-name">Team Room</span></a>`,
		`<p class="world-host">team-a</p>`,
		`<a href="/t/u/~/team-a/d/plans/" title="2 docs">plans/</a>`,
		`<li class="world-card gone">`,
		`<span class="world-name">old-world</span>`,
		`<li class="world-card federated">`,
		`federated · sign-in`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("universe missing %q", want)
		}
	}
}

func TestFloorSVGNodesAndLinks(t *testing.T) {
	tr := trail{Panes: []paneAddr{{Kind: paneFloor}}, Focus: 0}
	svg := string(floorSVG(testFloor(), tr, 0, DefaultTerms()))

	for _, want := range []string{
		`class="floor-world soma"`,
		// World node click → the world's stacks (root listing → rich index).
		`href="/t/u/~/team-a/d/"`, `data-node="w:team-a"`,
		// Unreachable world renders dimmed, present.
		`class="floor-world gone soma"`,
		`old-world`,
		`worlds · 0 portals`,
	} {
		if !strings.Contains(svg, want) {
			t.Errorf("floor svg missing %q", want)
		}
	}
	// Worlds only: no document satellites on the universe (ADR 0006 §5).
	if strings.Contains(svg, "floor-doc ") || strings.Contains(svg, "/index.md") {
		t.Errorf("universe must not draw documents: %s", svg)
	}
}

func TestFloorSVGEdgesAndPortals(t *testing.T) {
	floor := domain.Floor{
		Worlds: []domain.FloorWorld{
			{World: domain.WorldInfo{Name: "root", URL: "mark://root"}},
			{World: domain.WorldInfo{Name: "world-a"}},
			{World: domain.WorldInfo{Name: "wiki.example.org", URL: "mark://wiki.example.org"}, Portal: true},
		},
		Edges: []domain.Edge{
			{From: domain.Ref{World: "root"}, To: domain.Ref{World: "world-a"}, Count: 7},
			{From: domain.Ref{World: "world-a"}, To: domain.Ref{World: "wiki.example.org"}, Count: 1},
		},
	}
	svg := string(floorSVG(floor, trail{Panes: []paneAddr{{Kind: paneFloor}}, Focus: 0}, 0, DefaultTerms()))

	for _, want := range []string{
		// World-level edges in the map's grammar: directed, hoverable, bundled by count.
		`data-from="w:root" data-to="w:world-a"`, `edge-bundle`, `style="stroke-width:3.6"`,
		`data-from="w:world-a" data-to="w:wiki.example.org"`,
		`class="floor-portal-node soma"`, // external host as a portal node
		// Portal click opens that host's root (federation resolves the host).
		`href="/t/u/~/wiki.example.org/d/"`,
		"wiki.example.org",
		`1 portal`,
	} {
		if !strings.Contains(svg, want) {
			t.Errorf("floor svg missing %q", want)
		}
	}
	if n := strings.Count(svg, `<path class="graph-edge`); n != 2 {
		t.Errorf("edge count = %d, want 2", n)
	}
}

// Worlds ring the hub world (the one linked to at least half the others) and
// every world lands inside the viewBox.
func TestFloorSVGRingsAroundHub(t *testing.T) {
	var floor domain.Floor
	for _, n := range []string{"hub", "a", "b", "c", "d"} {
		floor.Worlds = append(floor.Worlds, domain.FloorWorld{World: domain.WorldInfo{Name: n}})
	}
	for _, n := range []string{"a", "b", "c"} {
		floor.Edges = append(floor.Edges, domain.Edge{From: domain.Ref{World: "hub"}, To: domain.Ref{World: n}, Count: 1})
	}
	svg := string(floorSVG(floor, trail{Panes: []paneAddr{{Kind: paneFloor}}, Focus: 0}, 0, DefaultTerms()))
	vb := regexp.MustCompile(`viewBox="0 0 (\d+) (\d+)"`).FindStringSubmatch(svg)
	if vb == nil {
		t.Fatalf("no viewBox in svg: %.200s", svg)
	}
	w, _ := strconv.Atoi(vb[1])
	h, _ := strconv.Atoi(vb[2])
	at := func(id string) (x, y int) {
		t.Helper()
		i := strings.Index(svg, `data-node="`+id+`"`)
		if i < 0 {
			t.Fatalf("no node %q in svg", id)
		}
		m := regexp.MustCompile(`cx="(-?\d+)" cy="(-?\d+)"`).FindStringSubmatch(svg[i:])
		if m == nil {
			t.Fatalf("node %q has no cx/cy", id)
		}
		x, _ = strconv.Atoi(m[1])
		y, _ = strconv.Atoi(m[2])
		return x, y
	}
	hx, hy := at("w:hub")
	if hx != w/2 || hy != wmTierTop+(h-wmTierTop-36)/2 {
		t.Errorf("hub should hold the centre, got (%d,%d) in %dx%d", hx, hy, w, h)
	}
	for _, n := range []string{"a", "b", "c", "d"} {
		x, y := at("w:" + n)
		if x <= 0 || y <= 0 || x >= w || y >= h || (x == hx && y == hy) {
			t.Errorf("world %s at (%d,%d) should sit on the ring inside %dx%d", n, x, y, w, h)
		}
	}
}

func TestFloorSVGEscapesContent(t *testing.T) {
	floor := domain.Floor{Worlds: []domain.FloorWorld{
		{World: domain.WorldInfo{Name: `<script>"evil"</script>`, URL: "mark://x"}},
	}}
	svg := string(floorSVG(floor, trail{Panes: []paneAddr{{Kind: paneFloor}}, Focus: 0}, 0, DefaultTerms()))
	if strings.Contains(svg, "<script>") {
		t.Errorf("unescaped name in svg: %s", svg)
	}
}

func TestTrailFloorPaneFocusedLive(t *testing.T) {
	svc := &fakeReading{floor: testFloor(), doc: domain.Document{Title: "Doc", Path: "/x.md", HTML: "<p>x</p>"}}
	rec := get(readingApp(t, svc), "/t/u/~/w.io/d/x.md")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	// Floor unfocused → cached; doc focused → live.
	want := "FloorCached,Read /x.md"
	if got := strings.Join(svc.calls, ","); got != want {
		t.Errorf("calls = %q, want %q", got, want)
	}
	// ADR 0006 §5: the universe renders worlds-only door cards by default
	// (the SVG topology is the secondary ?view=map).
	if !strings.Contains(rec.Body.String(), `class="world-card"`) {
		t.Errorf("floor body pane missing world cards")
	}

	svc2 := &fakeReading{floor: testFloor()}
	get(readingApp(t, svc2), "/t/u")
	if got := strings.Join(svc2.calls, ","); got != "Floor" {
		t.Errorf("focused floor calls = %q, want live Floor", got)
	}
}

func TestRootRedirectsToFloor(t *testing.T) {
	rec := get(readingApp(t, &fakeReading{}), "/")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/t/u" {
		t.Errorf("root -> %d %q, want 302 /t/u", rec.Code, rec.Header().Get("Location"))
	}
}

// The universe overlay shell (ADR 0006 §6) is embedded on the landing floor,
// and the floor's "view as map" link carries the universe-open class so
// islands.js summons the overlay (degrading to the inline ?view=map href).
func TestTrailUniverseOverlayShell(t *testing.T) {
	body := get(readingApp(t, &fakeReading{floor: testFloor()}), "/t/u").Body.String()
	if !strings.Contains(body, `id="universe-overlay"`) || !strings.Contains(body, `data-universe-url="/u?overlay=1"`) {
		t.Errorf("universe overlay shell missing: %s", body)
	}
	// Dialog semantics for screen readers (matches the graph overlay).
	if !strings.Contains(body, `role="dialog"`) || !strings.Contains(body, `aria-labelledby="universe-title"`) {
		t.Errorf("universe overlay missing ARIA dialog attributes: %s", body)
	}
	if !strings.Contains(body, `class="floor-view universe-open"`) {
		t.Errorf(`"view as map" trigger missing universe-open class: %s`, body)
	}
	// hx-boost="false" keeps htmx from boost-navigating the click so islands.js
	// can summon the overlay (the proven map-open/graph-open pattern).
	if !strings.Contains(body, `class="floor-view universe-open" href="/t/u?view=map" hx-boost="false"`) {
		t.Errorf(`"view as map" trigger must opt out of hx-boost: %s`, body)
	}
}

// /u?overlay=1 returns the bare floor SVG fragment (not a full page), with nodes
// extending the reader's current trail (from HX-Current-URL).
func TestFloorOverlayFragment(t *testing.T) {
	svc := &fakeReading{floor: testFloor()}
	rec := getFrom(readingApp(t, svc), "/u?overlay=1", "http://x/t/u")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `class="floor"`) || !strings.Contains(body, "<svg") {
		t.Errorf("overlay fragment should be the floor SVG: %s", body)
	}
	if strings.Contains(body, "<html") {
		t.Errorf("overlay fragment must be bare SVG, not a full page: %s", body)
	}
	// Live read (discovery action), not the cached floor.
	if got := strings.Join(svc.calls, ","); got != "Floor" {
		t.Errorf("overlay calls = %q, want live Floor", got)
	}
}

// A direct hit on /u without ?overlay=1 lands on the canvas floor — the floor
// has no standalone permalink.
func TestFloorPageRedirectsWhenNotOverlay(t *testing.T) {
	rec := get(readingApp(t, &fakeReading{floor: testFloor()}), "/u")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/t/u" {
		t.Errorf("/u -> %d %q, want 303 /t/u", rec.Code, rec.Header().Get("Location"))
	}
}

func TestTrailFloorErrorHandling(t *testing.T) {
	svc := &fakeReading{floorErr: domain.ErrUnauthorized}
	if rec := get(readingApp(t, svc), "/t/u"); rec.Code != http.StatusUnauthorized {
		t.Errorf("focused floor error -> %d, want 401", rec.Code)
	}
	svc = &fakeReading{floorErr: domain.ErrNotFound, doc: domain.Document{Title: "D", Path: "/x.md"}}
	rec := get(readingApp(t, svc), "/t/u/~/w.io/d/x.md")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `class="pane spine gone"`) {
		t.Errorf("unfocused floor error must tombstone: %d", rec.Code)
	}
}
