package web

import (
	"net/http"
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

func TestFloorCardsWorldsOnly(t *testing.T) {
	floor := testFloor()
	floor.Worlds = append(floor.Worlds, domain.FloorWorld{
		World: domain.WorldInfo{Name: "remote.example.org"}, Portal: true,
	})
	tr := trail{Panes: []paneAddr{{Kind: paneFloor}}, Focus: 0}
	out := string(floorCards(floor, tr, 0))

	// Worlds render as door cards; no loose documents at the universe level.
	if !strings.Contains(out, `class="world-card"`) {
		t.Error("expected world door cards")
	}
	if strings.Contains(out, "Hub") || strings.Contains(out, "ADR 0005") {
		t.Errorf("universe must list worlds only, not documents: %s", out)
	}
	// Entering a world lands on its stacks (root listing → rich index), not the
	// map pane — the map is the `m` discovery overlay (ADR 0006 §5).
	if !strings.Contains(out, `href="/t/u/~/team-a/d/"`) {
		t.Errorf("world card should enter the world's stacks: %s", out)
	}
	if strings.Contains(out, `/team-a/u/`) {
		t.Errorf("world card must not link to the map pane: %s", out)
	}
	// Federated/remote world → dashed federated door with an external root link.
	if !strings.Contains(out, "world-card federated") || !strings.Contains(out, "federated · sign-in") {
		t.Errorf("portal world should render as a federated door: %s", out)
	}
	// Unreadable world still renders, tagged (absence would read as nonexistence).
	if !strings.Contains(out, "unreadable") {
		t.Errorf("unreadable world should render, tagged: %s", out)
	}
}

func TestFloorSVGNodesAndLinks(t *testing.T) {
	tr := trail{Panes: []paneAddr{{Kind: paneFloor}}, Focus: 0}
	svg := string(floorSVG(testFloor(), tr, 0))

	for _, want := range []string{
		`class="floor-world"`,
		// World node click → the world's stacks (root listing → rich index).
		`href="/t/u/~/team-a/d/"`,
		// Doc node click → the document pane.
		`href="/t/u/~/team-a/d/index.md"`,
		`class="floor-doc status-accepted"`,
		// Unreachable world renders dimmed, present.
		`class="floor-system gone"`,
		`old-world`,
		// Long titles trim in the label; full title rides the tooltip.
		"ADR 0005 — a very l…",
		"ADR 0005 — a very long title that needs trimming — /adr/0005.md",
	} {
		if !strings.Contains(svg, want) {
			t.Errorf("floor svg missing %q", want)
		}
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
			{From: domain.Ref{World: "root"}, To: domain.Ref{World: "world-a"}},
			{From: domain.Ref{World: "world-a"}, To: domain.Ref{World: "wiki.example.org"}},
		},
	}
	svg := string(floorSVG(floor, trail{Panes: []paneAddr{{Kind: paneFloor}}, Focus: 0}, 0))

	for _, want := range []string{
		`class="floor-edge"`,        // world-level edges drawn
		`class="floor-portal-node"`, // external host as a portal node
		// Portal click opens that host's root (federation resolves the host).
		`href="/t/u/~/wiki.example.org/d/"`,
		"wiki.example.org",
	} {
		if !strings.Contains(svg, want) {
			t.Errorf("floor svg missing %q", want)
		}
	}
	// Two edges → two <line> elements.
	if n := strings.Count(svg, "floor-edge"); n != 2 {
		t.Errorf("edge count = %d, want 2", n)
	}
}

func TestFloorSVGEscapesContent(t *testing.T) {
	floor := domain.Floor{Worlds: []domain.FloorWorld{
		{World: domain.WorldInfo{Name: "w"},
			Docs: []domain.FloorDoc{{Path: "/x.md", Title: `<script>"evil"</script>`, Importance: 0.5, Status: "draft"}}},
	}}
	svg := string(floorSVG(floor, trail{Panes: []paneAddr{{Kind: paneFloor}}, Focus: 0}, 0))
	if strings.Contains(svg, "<script>") {
		t.Errorf("unescaped title in svg: %s", svg)
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
