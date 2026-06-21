package service

import (
	"context"
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
	dirs := make([]string, 0, len(clusters))
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

// A hub graph export over the same paths as worldMapCatalog: every doc is a
// node, but only a→b (intra-world) and adr/0001→root/index (cross-world) are
// reference edges. So index.md and plans/c.md are reference-orphans; a, b, and
// adr/0001 (linked off-world) are not.
const worldMapHubGraph = `
## Nodes

| URL | Title | Status | Links |
|-----|-------|--------|-------|
| [Home](mark://world-a.svc:6309/index.md)    | Home   | accepted | 0 |
| [Plan A](mark://world-a.svc:6309/plans/a.md) | Plan A | draft    | 1 |
| [Plan B](mark://world-a.svc:6309/plans/b.md) | Plan B | draft    | 0 |
| [Plan C](mark://world-a.svc:6309/plans/c.md) | Plan C | draft    | 0 |
| [ADR 1](mark://world-a.svc:6309/adr/0001.md) | ADR 1  | accepted | 1 |

## Edges

| From | To |
|------|-----|
| mark://world-a.svc:6309/plans/a.md  | mark://world-a.svc:6309/plans/b.md |
| mark://world-a.svc:6309/adr/0001.md | mark://root.svc:6309/index.md      |
`

func TestWorldOrphans(t *testing.T) {
	host2name := map[string]string{"world-a.svc:6309": "world-a"}
	topo := parseGraphExport(worldMapHubGraph)

	got := worldOrphans("world-a", host2name, topo)
	want := map[string]bool{"/index.md": true, "/plans/c.md": true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("orphans = %+v, want %+v", got, want)
	}

	// No durable topology ⇒ nothing flagged (unknown, not orphan).
	if o := worldOrphans("world-a", host2name, hubTopology{}); o != nil {
		t.Errorf("empty topology should flag nothing, got %+v", o)
	}
	// A topology that knows no node in this world ⇒ also nothing flagged.
	if o := worldOrphans("world-b", host2name, topo); o != nil {
		t.Errorf("topology with no world-b node should flag nothing, got %+v", o)
	}
}

// A document links its own world without the default port (mark://host/...),
// while the world is named with it (host:6309). The topology join must be
// port-stable, or hub edges never reach the world map and the floor sprouts a
// phantom port-less portal beside the real world. Regression for that bug.
func TestHostJoinIsPortStable(t *testing.T) {
	// Port-less ref host joins the explicit-default-port world via host2name.
	if !worldMember("world-a.svc", "world-a", map[string]string{"world-a.svc:6309": "world-a"}) {
		t.Error("port-less ref host should join its default-port world via host2name")
	}
	// Two host-shaped names differing only by the implicit default port match.
	if !worldMember("soul.demarkus.io", "soul.demarkus.io:6309", map[string]string{}) {
		t.Error("port-less host should match the same host named with the default port")
	}
	// An explicit non-default port stays distinct (a different dev world on one host).
	if worldMember("localhost:6401", "localhost:6309", map[string]string{}) {
		t.Error("explicit non-default port must not collapse onto the default-port world")
	}

	// hostKey appends the default port only when one is absent.
	for in, want := range map[string]string{
		"soul.demarkus.io":      "soul.demarkus.io:6309",
		"soul.demarkus.io:6309": "soul.demarkus.io:6309",
		"localhost:6401":        "localhost:6401",
		"":                      "",
		// IPv6: a bare literal must be bracketed before the default port; a
		// bracketed literal that already carries a port is left alone.
		"2001:db8::1":        "[2001:db8::1]:6309",
		"[2001:db8::1]:6310": "[2001:db8::1]:6310",
	} {
		if got := hostKey(in); got != want {
			t.Errorf("hostKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWorldMapMarksOrphansFromHubGraph(t *testing.T) {
	gw := fakeGateway{
		worlds:    []domain.WorldInfo{{Name: "world-a", URL: "mark://world-a.svc:6309"}},
		raw:       domain.RawDocument{Body: worldMapCatalog},
		fetchBody: map[string]string{hubGraphPath: worldMapHubGraph},
	}
	wm, err := NewReadingService(gw, fakeRenderer{}, nil).WithHub("root").WorldMap(t.Context(), "world-a")
	if err != nil {
		t.Fatalf("WorldMap: %v", err)
	}
	orphan := map[string]bool{}
	for _, cl := range wm.Clusters {
		for _, d := range cl.Docs {
			orphan[d.Path] = d.Orphan
		}
	}
	want := map[string]bool{
		"/index.md":    true,
		"/plans/a.md":  false,
		"/plans/b.md":  false,
		"/plans/c.md":  true,
		"/adr/0001.md": false, // linked off-world → not an orphan
	}
	if !reflect.DeepEqual(orphan, want) {
		t.Errorf("orphan flags = %+v, want %+v", orphan, want)
	}
}

func TestWorldMapNoHubMarksNoOrphans(t *testing.T) {
	// Without a hub topology the map still assembles, but flags no orphans —
	// zero observed edges is honest cold state, not a defect (ADR 0005 d8).
	gw := fakeGateway{
		worlds: []domain.WorldInfo{{Name: "world-a", URL: "mark://world-a.svc:6309"}},
		raw:    domain.RawDocument{Body: worldMapCatalog},
	}
	wm, err := NewReadingService(gw, fakeRenderer{}, nil).WorldMap(t.Context(), "world-a")
	if err != nil {
		t.Fatalf("WorldMap: %v", err)
	}
	for _, cl := range wm.Clusters {
		for _, d := range cl.Docs {
			if d.Orphan {
				t.Errorf("%s flagged orphan without a hub topology", d.Path)
			}
		}
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

func TestWorldMapDegradesOnReadError(t *testing.T) {
	// A catalog-read failure (old/unreachable world, rejected query) degrades to
	// an Unreadable map — no 502 — like the floor tombstoning a world.
	svc := NewReadingService(fakeGateway{err: errTest}, fakeRenderer{}, nil)
	wm, err := svc.WorldMap(t.Context(), "world-a")
	if err != nil {
		t.Fatalf("WorldMap should degrade, not error: %v", err)
	}
	if !wm.Unreadable || len(wm.Clusters) != 0 || wm.World.Name != "world-a" {
		t.Errorf("unreadable map = %+v, want Unreadable, no clusters, name world-a", wm)
	}
	// The unreadable result is not cached — a later read retries the world.
	if _, ok := svc.worldMaps.getFresh("world-a", worldMapTTL); ok {
		t.Error("unreadable map must not be cached")
	}

	// ErrUnauthorized (re-login) and context cancellation/timeout must
	// propagate, never degrade — a terminated read is not an "unreadable" map.
	for _, prop := range []error{domain.ErrUnauthorized, context.Canceled, context.DeadlineExceeded} {
		_, err = NewReadingService(fakeGateway{err: prop}, fakeRenderer{}, nil).WorldMap(t.Context(), "world-a")
		if !errors.Is(err, prop) {
			t.Errorf("%v must propagate, got %v", prop, err)
		}
	}
}
