package service

import (
	"reflect"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// stubGraphParser returns a fixed domain topology, keeping core tests
// port-only; export decoding is covered by the graphexport adapter tests.
type stubGraphParser struct {
	nodes []domain.GraphNode
	edges []domain.Edge
}

func (p stubGraphParser) ParseGraphExport(string) ([]domain.GraphNode, []domain.Edge) {
	return p.nodes, p.edges
}

// hubStubTopology mirrors a hub graph export: two authorized-world docs plus
// a cross-host edge to an external wiki (the portal the floor tests expect).
var hubStubTopology = stubGraphParser{
	nodes: []domain.GraphNode{
		{Ref: domain.Ref{World: "root.svc:6309", Path: "/index.md"}, Status: "ok"},
		{Ref: domain.Ref{World: "world-a.svc:6309", Path: "/guide.md"}, Status: "ok"},
	},
	edges: []domain.Edge{
		{From: domain.Ref{World: "root.svc:6309", Path: "/index.md"}, To: domain.Ref{World: "world-a.svc:6309", Path: "/guide.md"}, Type: domain.EdgeReference},
		{From: domain.Ref{World: "world-a.svc:6309", Path: "/guide.md"}, To: domain.Ref{World: "wiki.example.org", Path: "/notes.md"}, Type: domain.EdgeReference},
	},
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

func TestWorldEdgesMatchesAuthorizedWorldByNamePort(t *testing.T) {
	// The hub is referenced by name+port (mark://root:6309, a port-normalized
	// mark://root/... link) — it matches neither the authorized name "root" nor
	// root's dial address, so without the name+port fallback it would double as
	// a "root" portal beside the real world.
	host2name := map[string]string{"world-a.svc:6309": "world-a"}
	authorized := map[string]bool{"root": true, "world-a": true}
	edges := []domain.Edge{
		{From: domain.Ref{World: "world-a.svc:6309", Path: "/index.md"}, To: domain.Ref{World: "root:6309", Path: "/index.md"}},
	}
	got, portals := worldEdges(edges, host2name, authorized)

	if len(portals) != 0 {
		t.Errorf("portals = %+v, want none (root:6309 must join the authorized hub)", portals)
	}
	want := []domain.Edge{{From: domain.Ref{World: "world-a"}, To: domain.Ref{World: "root"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("edges = %+v, want %+v", got, want)
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

func TestPortalLabel(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"soul.demarkus.io", "soul.demarkus.io", true},         // port-less → kept
		{"soul.demarkus.io:6309", "soul.demarkus.io", true},    // default port elided (collapses with the above)
		{"dev.example.org:6401", "dev.example.org:6401", true}, // explicit non-default port kept
		{"localhost", "", false},                               // dev/crawl artifacts → dropped
		{"localhost:6309", "", false},
		{"127.0.0.1:6401", "", false},               // IPv4 loopback
		{"0.0.0.0:6309", "", false},                 // unspecified
		{"[::1]:6309", "", false},                   // bracketed IPv6 loopback + port
		{"::1", "", false},                          // bare IPv6 loopback
		{"[::1]", "", false},                        // bracketed IPv6 loopback, no port
		{"cache.svc.cluster.local:6309", "", false}, // cluster-internal
		{"10.0.0.5:6309", "10.0.0.5", true},         // private IP KEPT (LAN federation)
		{"[2001:db8::5]:6309", "2001:db8::5", true}, // routable IPv6 KEPT
	}
	for _, c := range cases {
		got, ok := portalLabel(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("portalLabel(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestWorldEdgesCanonicalizesAndFiltersPortals(t *testing.T) {
	authorized := map[string]bool{"root": true}
	edges := []domain.Edge{
		// the same external host in port-less and explicit-port form → ONE portal.
		{From: domain.Ref{World: "root", Path: "/a.md"}, To: domain.Ref{World: "soul.demarkus.io", Path: "/x.md"}},
		{From: domain.Ref{World: "root", Path: "/b.md"}, To: domain.Ref{World: "soul.demarkus.io:6309", Path: "/y.md"}},
		// loopback / localhost / private / cluster-internal endpoints drop their edge.
		{From: domain.Ref{World: "root", Path: "/c.md"}, To: domain.Ref{World: "localhost", Path: "/z.md"}},
		{From: domain.Ref{World: "root", Path: "/d.md"}, To: domain.Ref{World: "localhost:6309", Path: "/z.md"}},
		{From: domain.Ref{World: "root", Path: "/e.md"}, To: domain.Ref{World: "127.0.0.1:6401", Path: "/z.md"}},
		{From: domain.Ref{World: "root", Path: "/f.md"}, To: domain.Ref{World: "cache.svc.cluster.local:6309", Path: "/z.md"}},
		// an explicit non-default port is a distinct, navigable portal → kept.
		{From: domain.Ref{World: "root", Path: "/g.md"}, To: domain.Ref{World: "dev.example.org:6401", Path: "/z.md"}},
	}
	gotEdges, portals := worldEdges(edges, nil, authorized)

	wantPortals := []string{"dev.example.org:6401", "soul.demarkus.io"}
	if !reflect.DeepEqual(portals, wantPortals) {
		t.Errorf("portals = %+v, want %+v", portals, wantPortals)
	}
	wantEdges := []domain.Edge{
		{From: domain.Ref{World: "root"}, To: domain.Ref{World: "dev.example.org:6401"}},
		{From: domain.Ref{World: "root"}, To: domain.Ref{World: "soul.demarkus.io"}},
	}
	if !reflect.DeepEqual(gotEdges, wantEdges) {
		t.Errorf("edges = %+v, want %+v", gotEdges, wantEdges)
	}
}

func TestFloorEnrichedWithHubEdgesAndPortals(t *testing.T) {
	gw := fakeGateway{
		worlds: []domain.WorldInfo{
			{Name: "root", URL: "mark://root.svc:6309"},
			{Name: "world-a", URL: "mark://world-a.svc:6309"},
		},
		raw:       domain.RawDocument{Body: lookupTable}, // Lookup → satellites
		fetchBody: map[string]string{hubGraphPath: "stub"},
	}
	svc := NewReadingService(gw, fakeRenderer{}, nil).WithHub("root", hubStubTopology)

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
	stub := stubGraphParser{edges: []domain.Edge{{
		From: domain.Ref{World: "world-a.world-a.svc:6309", Path: "/index.md"},
		To:   domain.Ref{World: "root.root.svc:6309", Path: "/index.md"},
		Type: domain.EdgeReference,
	}}}
	gw := fakeGateway{
		worlds: []domain.WorldInfo{
			{Name: "root", Address: "mark://root.root.svc:6309"},
			{Name: "world-a", Address: "mark://world-a.world-a.svc:6309"},
		},
		raw:       domain.RawDocument{Body: lookupTable},
		fetchBody: map[string]string{hubGraphPath: "stub"},
	}
	floor, err := NewReadingService(gw, fakeRenderer{}, nil).WithHub("root", stub).Floor(t.Context())
	if err != nil {
		t.Fatalf("Floor: %v", err)
	}
	if len(floor.Edges) != 1 ||
		floor.Edges[0] != (domain.Edge{From: domain.Ref{World: "world-a"}, To: domain.Ref{World: "root"}, Type: domain.EdgeReference}) {
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
