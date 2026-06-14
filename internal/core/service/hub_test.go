package service

import (
	"reflect"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// A trimmed mark_graph_export document: a Nodes table and an Edges table, with
// an external (https) row and a separator row that must both be ignored.
const graphExport = `# Document Graph

> Nodes: 4
> Edges: 3

## Nodes

| URL | Title | Status | Links |
|-----|-------|--------|-------|
| [https://github.com/x](https://github.com/x) |  | external | 0 |
| [mark://root.svc:6309/index.md](mark://root.svc:6309/index.md) | Root | ok | 2 |
| [mark://world-a.svc:6309/guide.md](mark://world-a.svc:6309/guide.md) | Guide | ok | 1 |

## Edges

| From | To |
|------|----|
| mark://root.svc:6309/index.md | mark://world-a.svc:6309/guide.md |
| mark://world-a.svc:6309/guide.md | mark://wiki.example.org/notes.md |
| https://github.com/x | mark://root.svc:6309/index.md |
`

func TestParseGraphExport(t *testing.T) {
	g := parseGraphExport(graphExport)

	if len(g.nodes) != 2 {
		t.Fatalf("nodes = %d, want 2 (external skipped): %+v", len(g.nodes), g.nodes)
	}
	if g.nodes[0].Ref != (domain.Ref{World: "root.svc:6309", Path: "/index.md"}) || g.nodes[0].Status != "ok" {
		t.Errorf("nodes[0] = %+v", g.nodes[0])
	}
	// Two mark://→mark:// edges; the https→mark row is dropped.
	want := []domain.Edge{
		{From: domain.Ref{World: "root.svc:6309", Path: "/index.md"}, To: domain.Ref{World: "world-a.svc:6309", Path: "/guide.md"}},
		{From: domain.Ref{World: "world-a.svc:6309", Path: "/guide.md"}, To: domain.Ref{World: "wiki.example.org", Path: "/notes.md"}},
	}
	if !reflect.DeepEqual(g.edges, want) {
		t.Errorf("edges = %+v, want %+v", g.edges, want)
	}
}

func TestParseMarkRef(t *testing.T) {
	cases := map[string]domain.Ref{
		"mark://h:6309/a/b.md": {World: "h:6309", Path: "/a/b.md"},
		"mark://h:6309/":       {World: "h:6309", Path: "/"},
		"mark://h:6309":        {World: "h:6309", Path: "/"},
		"mark://Host/X.md":     {World: "host", Path: "/X.md"}, // host lowercased, path kept
	}
	for in, want := range cases {
		if got, ok := parseMarkRef(in); !ok || got != want {
			t.Errorf("parseMarkRef(%q) = %+v ok=%v, want %+v", in, got, ok, want)
		}
	}
	for _, bad := range []string{"https://x.com/a", "", "mark://", "  | From "} {
		if _, ok := parseMarkRef(bad); ok {
			t.Errorf("parseMarkRef(%q) accepted", bad)
		}
	}
}

func TestWorldEdgesJoinsHostsAndFindsPortals(t *testing.T) {
	// host→name from mark_worlds; root.svc and world-a.svc are authorized,
	// wiki.example.org is not → a portal.
	host2name := map[string]string{"root.svc:6309": "root", "world-a.svc:6309": "world-a"}
	authorized := map[string]bool{"root": true, "world-a": true}
	edges := []domain.Edge{
		{From: domain.Ref{World: "root.svc:6309", Path: "/index.md"}, To: domain.Ref{World: "world-a.svc:6309", Path: "/guide.md"}},
		{From: domain.Ref{World: "world-a.svc:6309", Path: "/guide.md"}, To: domain.Ref{World: "wiki.example.org", Path: "/notes.md"}},
		// intra-world edge (same world both ends) → dropped.
		{From: domain.Ref{World: "root.svc:6309", Path: "/a.md"}, To: domain.Ref{World: "root.svc:6309", Path: "/b.md"}},
		// duplicate of the first world-pair → deduped.
		{From: domain.Ref{World: "root.svc:6309", Path: "/x.md"}, To: domain.Ref{World: "world-a.svc:6309", Path: "/y.md"}},
	}
	got, portals := worldEdges(edges, host2name, authorized)

	wantEdges := []domain.Edge{
		{From: domain.Ref{World: "root"}, To: domain.Ref{World: "world-a"}},
		{From: domain.Ref{World: "world-a"}, To: domain.Ref{World: "wiki.example.org"}},
	}
	if !reflect.DeepEqual(got, wantEdges) {
		t.Errorf("edges = %+v, want %+v", got, wantEdges)
	}
	if !reflect.DeepEqual(portals, []string{"wiki.example.org"}) {
		t.Errorf("portals = %+v, want [wiki.example.org]", portals)
	}
}

func TestWorldEdgesObservedIdsPassThrough(t *testing.T) {
	// Observed-map edges already carry the library's own world ids (names),
	// so they need no host→name join; an unauthorized one is still a portal.
	authorized := map[string]bool{"root": true}
	edges := []domain.Edge{
		{From: domain.Ref{World: "root", Path: "/a.md"}, To: domain.Ref{World: "ext.io", Path: "/b.md"}},
	}
	got, portals := worldEdges(edges, nil, authorized)
	if len(got) != 1 || got[0] != (domain.Edge{From: domain.Ref{World: "root"}, To: domain.Ref{World: "ext.io"}}) {
		t.Errorf("edges = %+v", got)
	}
	if !reflect.DeepEqual(portals, []string{"ext.io"}) {
		t.Errorf("portals = %+v", portals)
	}
}

func TestFloorEnrichedWithHubEdgesAndPortals(t *testing.T) {
	gw := fakeGateway{
		worlds: []domain.WorldInfo{
			{Name: "root", URL: "mark://root.svc:6309"},
			{Name: "world-a", URL: "mark://world-a.svc:6309"},
		},
		raw:       domain.RawDocument{Body: lookupTable}, // Lookup → satellites
		fetchBody: map[string]string{hubGraphPath: graphExport},
	}
	svc := NewReadingService(gw, fakeRenderer{}, nil).WithHub("root")

	floor, err := svc.Floor(t.Context())
	if err != nil {
		t.Fatalf("Floor: %v", err)
	}
	// Two authorized worlds + one portal (wiki.example.org from the cross-host edge).
	var systems, portals int
	for _, w := range floor.Worlds {
		if w.Portal {
			portals++
		} else {
			systems++
		}
	}
	if systems != 2 || portals != 1 {
		t.Fatalf("systems=%d portals=%d, want 2/1 (%+v)", systems, portals, floor.Worlds)
	}
	// Edges: root→world-a and world-a→wiki.example.org, masked + joined.
	if len(floor.Edges) != 2 {
		t.Errorf("edges = %d, want 2 (%+v)", len(floor.Edges), floor.Edges)
	}
}

func TestFloorJoinsCrossWorldEdgeViaAddress(t *testing.T) {
	// Worlds carry their internal dial Address (mark_worlds' address column),
	// which is how the hub graph keys their nodes. A cross-world edge between
	// two authorized worlds must then join cluster-to-cluster (name→name) with
	// NO portal — the host↔name join the address column exists to enable.
	graph := "# Document Graph\n\n## Edges\n\n| From | To |\n|------|----|\n" +
		"| mark://world-a.world-a.svc:6309/index.md | mark://root.root.svc:6309/index.md |\n"
	gw := fakeGateway{
		worlds: []domain.WorldInfo{
			{Name: "root", Address: "mark://root.root.svc:6309"},
			{Name: "world-a", Address: "mark://world-a.world-a.svc:6309"},
		},
		raw:       domain.RawDocument{Body: lookupTable},
		fetchBody: map[string]string{hubGraphPath: graph},
	}
	floor, err := NewReadingService(gw, fakeRenderer{}, nil).WithHub("root").Floor(t.Context())
	if err != nil {
		t.Fatalf("Floor: %v", err)
	}
	if len(floor.Edges) != 1 ||
		floor.Edges[0] != (domain.Edge{From: domain.Ref{World: "world-a"}, To: domain.Ref{World: "root"}}) {
		t.Errorf("edges = %+v, want one world-a→root (joined by address)", floor.Edges)
	}
	for _, w := range floor.Worlds {
		if w.Portal {
			t.Errorf("unexpected portal %q — crawl host should have joined its cluster", w.World.Name)
		}
	}
}

func TestFloorNoHubDegradesToBaseline(t *testing.T) {
	// No hub set → no fetch of a graph doc, no edges, no portals; the floor is
	// exactly the mark_worlds + lookup baseline.
	svc := NewReadingService(fakeGateway{
		worlds: []domain.WorldInfo{{Name: "root"}},
		raw:    domain.RawDocument{Body: lookupTable},
	}, fakeRenderer{}, nil)
	floor, err := svc.Floor(t.Context())
	if err != nil {
		t.Fatalf("Floor: %v", err)
	}
	if len(floor.Worlds) != 1 || floor.Worlds[0].Portal || floor.Edges != nil {
		t.Errorf("baseline floor changed: %+v edges=%+v", floor.Worlds, floor.Edges)
	}
}
