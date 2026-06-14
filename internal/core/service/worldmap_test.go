package service

import (
	"errors"
	"reflect"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

var errTest = errors.New("test failure")

// A catalog with two directories plus root-level docs, enough rows in one dir
// to force a "+N more" aggregate at a small cap.
const worldMapCatalog = `
# Lookup matches for "*" in /

| Path | Importance | Title | Tags |
|------|------------|-------|------|
| /index.md        | 0.95 | Home   | hub,status:accepted |
| /plans/a.md      | 0.80 | Plan A | plans |
| /plans/b.md      | 0.70 | Plan B | plans |
| /plans/c.md      | 0.60 | Plan C | plans |
| /adr/0001.md     | 0.90 | ADR 1  | adr,status:accepted |
`

func TestWorldClustersGroupsByTopDirWithAggregate(t *testing.T) {
	docs := parseCatalogTable(worldMapCatalog, 100)
	clusters := worldClusters(docs, 2) // cap 2 labeled docs per cluster

	// Root cluster first, then "adr", then "plans" (alphabetical).
	var dirs []string
	for _, c := range clusters {
		dirs = append(dirs, c.Dir)
	}
	if !reflect.DeepEqual(dirs, []string{"", "adr", "plans"}) {
		t.Fatalf("dirs = %v, want [\"\" adr plans]", dirs)
	}

	root := clusters[0]
	if root.ListPath != "/" || len(root.Docs) != 1 || root.More != 0 {
		t.Errorf("root cluster = %+v", root)
	}
	plans := clusters[2]
	if plans.ListPath != "/plans/" {
		t.Errorf("plans ListPath = %q, want /plans/", plans.ListPath)
	}
	// 3 plans docs, cap 2 → 2 labeled + 1 aggregated.
	if len(plans.Docs) != 2 || plans.More != 1 {
		t.Errorf("plans cluster = %+v, want 2 docs + More 1", plans)
	}
	// Importance order preserved within the cluster.
	if plans.Docs[0].Path != "/plans/a.md" || plans.Docs[1].Path != "/plans/b.md" {
		t.Errorf("plans docs out of importance order: %+v", plans.Docs)
	}
}

func TestTopDir(t *testing.T) {
	cases := map[string]string{
		"/index.md":         "",
		"/plans/reading.md": "plans",
		"/a/b/c.md":         "a",
		"index.md":          "", // tolerate a missing leading slash
	}
	for path, want := range cases {
		if got := topDir(path); got != want {
			t.Errorf("topDir(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestIntraWorldEdgesJoinAndFilter(t *testing.T) {
	// Labeled (rendered) docs: a.md, b.md. c.md is aggregated (not labeled).
	labeled := map[string]bool{"/plans/a.md": true, "/plans/b.md": true}
	host2name := map[string]string{"world-a.svc:6309": "world-a"}
	edges := []domain.Edge{
		// Observed (name-keyed) intra-world edge between two labeled docs — kept.
		{From: domain.Ref{World: "world-a", Path: "/plans/a.md"}, To: domain.Ref{World: "world-a", Path: "/plans/b.md"}},
		// Hub (host-keyed) edge — joins via host2name, also kept (deduped if same).
		{From: domain.Ref{World: "world-a.svc:6309", Path: "/plans/b.md"}, To: domain.Ref{World: "world-a.svc:6309", Path: "/plans/a.md"}},
		// Endpoint not labeled (aggregated) → dropped.
		{From: domain.Ref{World: "world-a", Path: "/plans/a.md"}, To: domain.Ref{World: "world-a", Path: "/plans/c.md"}},
		// Cross-world edge → dropped (not intra-world).
		{From: domain.Ref{World: "world-a", Path: "/plans/a.md"}, To: domain.Ref{World: "root", Path: "/index.md"}},
	}
	got := intraWorldEdges("world-a", host2name, edges, labeled)
	want := []domain.Edge{
		{From: domain.Ref{World: "world-a", Path: "/plans/a.md"}, To: domain.Ref{World: "world-a", Path: "/plans/b.md"}},
		{From: domain.Ref{World: "world-a", Path: "/plans/b.md"}, To: domain.Ref{World: "world-a", Path: "/plans/a.md"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("edges = %+v, want %+v", got, want)
	}
}

func TestWorldMapAssemblesAndCaches(t *testing.T) {
	gw := fakeGateway{
		worlds: []domain.WorldInfo{{Name: "world-a", URL: "mark://world-a.svc:6309"}},
		raw:    domain.RawDocument{Body: worldMapCatalog},
	}
	svc := NewReadingService(gw, fakeRenderer{}, nil)

	wm, err := svc.WorldMap(t.Context(), "world-a")
	if err != nil {
		t.Fatalf("WorldMap: %v", err)
	}
	if wm.World.Name != "world-a" || wm.World.URL != "mark://world-a.svc:6309" {
		t.Errorf("world identity = %+v", wm.World)
	}
	if len(wm.Clusters) != 3 {
		t.Fatalf("clusters = %d, want 3 (%+v)", len(wm.Clusters), wm.Clusters)
	}
	// Cached read returns the same assembly.
	cached, err := svc.WorldMapCached(t.Context(), "world-a")
	if err != nil {
		t.Fatalf("WorldMapCached: %v", err)
	}
	if !reflect.DeepEqual(cached.Clusters, wm.Clusters) {
		t.Errorf("cached clusters differ from live")
	}
}

func TestWorldMapEnrichmentIsBestEffort(t *testing.T) {
	// Worlds() fails (enrichment), but the catalog reads — the map still builds,
	// with a name-only world identity and no edges.
	gw := fakeGateway{
		worldsErr: errTest,
		raw:       domain.RawDocument{Body: worldMapCatalog},
	}
	wm, err := NewReadingService(gw, fakeRenderer{}, nil).WorldMap(t.Context(), "world-a")
	if err != nil {
		t.Fatalf("WorldMap should not fail on enrichment error: %v", err)
	}
	if wm.World.Name != "world-a" || len(wm.Clusters) != 3 || wm.Edges != nil {
		t.Errorf("degraded map = %+v", wm)
	}
}
