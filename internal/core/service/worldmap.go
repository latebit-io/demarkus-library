package service

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// The world map (zoom level 2; plans §"Universe-view research" + §"World-view
// zoom level"). The floor zoomed into one world: its catalog grouped into
// top-level directory clusters, each showing its top-importance documents as
// labeled nodes with a "+N more" aggregate that opens the dir's listing pane.
// Same MCP-readable channel as the floor (mark_lookup "*") — the projection
// adds layout, never information (ADR 0005 decision 11).
const (
	// worldMapMaxDocs caps the catalog parse — a world map is a navigation
	// aid, not the whole stacks (the listing panes hold the long tail).
	worldMapMaxDocs = 500
	// worldMapClusterDocs caps labeled nodes per directory cluster; the rest
	// collapse into the "+N more" aggregate bubble.
	worldMapClusterDocs = 12
)

// worldMapTTL bounds how long a cached world map serves an unfocused pane
// before it rebuilds — mirrors floorTTL. Without it an unfocused world-map pane
// would serve the first-ever assembly forever, so a freshly published or edited
// document would never appear there.
const worldMapTTL = floorTTL

// worldMapCache holds the last assembled map per world, each timestamped for the
// TTL. Mirrors floorCache; the focused path (WorldMap) rebuilds live, the cache
// serves unfocused panes within the TTL window.
type worldMapCache struct {
	mu  sync.Mutex
	all map[string]worldMapEntry
	now func() time.Time // nil ⇒ time.Now (overridable in tests)
}

type worldMapEntry struct {
	wm       domain.WorldMap
	storedAt time.Time
}

func (c *worldMapCache) clockNow() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// getFresh returns the cached map for world only when it is younger than ttl.
func (c *worldMapCache) getFresh(world string, ttl time.Duration) (domain.WorldMap, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.all[world]
	if !ok || c.clockNow().Sub(e.storedAt) >= ttl {
		return domain.WorldMap{}, false
	}
	return e.wm, true
}

func (c *worldMapCache) put(world string, wm domain.WorldMap) {
	c.mu.Lock()
	if c.all == nil {
		c.all = make(map[string]worldMapEntry)
	}
	c.all[world] = worldMapEntry{wm: wm, storedAt: c.clockNow()}
	c.mu.Unlock()
}

// invalidate drops a world's cached map so the next read rebuilds it — called
// after a write to that world so a just-published document appears at once.
func (c *worldMapCache) invalidate(world string) {
	c.mu.Lock()
	delete(c.all, world)
	c.mu.Unlock()
}

// WorldMap assembles one world's map live: its whole catalog ("*", importance
// order) grouped into top-level directory clusters, plus the intra-world edges
// among the rendered documents (observed-links map ∪ hub graph). The world
// identity and edges are best-effort enrichment that degrades to nothing.
//
// A catalog-read failure degrades rather than failing the view, with two
// exceptions that propagate: ErrUnauthorized (the reader's identity dying →
// re-login) and context cancellation/timeout (the request must terminate, not
// render a map). Any other error (an old or unreachable world, a rejected
// query) returns an Unreadable map so the page renders a notice instead of
// 502'ing — the same posture as the floor tombstoning a single unreadable
// world rather than dropping the whole map. The unreadable result is not
// cached, so a transient failure self-heals on the next read.
func (s *ReadingService) WorldMap(ctx context.Context, world string) (domain.WorldMap, error) {
	raw, err := s.world.Lookup(ctx, world, "/", "*", "")
	if err != nil {
		// Propagate, never degrade: the reader's identity dying (re-login) and
		// request cancellation/timeout — a canceled or timed-out read must
		// terminate, not render an "unreadable" map. Any other read failure (an
		// old/unreachable world, a rejected query) degrades.
		if errors.Is(err, domain.ErrUnauthorized) ||
			errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return domain.WorldMap{}, err
		}
		return domain.WorldMap{World: domain.WorldInfo{Name: world}, Unreadable: true}, nil
	}
	docs := parseCatalogTable(raw.Body, worldMapMaxDocs)
	clusters := worldClusters(docs, worldMapClusterDocs)

	wm := domain.WorldMap{World: domain.WorldInfo{Name: world}, Clusters: clusters}

	// Enrichment (best-effort): the world's display identity (its mark:// URL)
	// and the host→name join key for intra-world hub edges.
	host2name := map[string]string{}
	if worlds, werr := s.world.Worlds(ctx); werr == nil {
		for _, w := range worlds {
			if w.Name == world {
				wm.World = w
			}
			joinAddr := w.Address
			if joinAddr == "" {
				joinAddr = w.URL
			}
			if h := hostOf(joinAddr); h != "" {
				host2name[h] = w.Name
			}
		}
	}

	// Edges drawn only among the labeled (rendered) nodes — an edge can't point
	// at a node the map doesn't draw. Source: the durable hub graph ∪ the R3
	// observed map, both filtered to this world (the floor drops these
	// intra-world edges; the world map is where they belong).
	labeled := map[string]bool{}
	for _, cl := range clusters {
		for _, d := range cl.Docs {
			labeled[d.Path] = true
		}
	}
	wm.Edges = intraWorldEdges(world, host2name,
		append(s.readHub(ctx, s.hub).edges, s.graph.allEdges()...), labeled)

	s.worldMaps.put(world, wm)
	return wm, nil
}

// WorldMapCached serves the last assembled map for world, building live on a
// miss. Unfocused world-map panes read here — the focused-live policy every
// other pane follows.
func (s *ReadingService) WorldMapCached(ctx context.Context, world string) (domain.WorldMap, error) {
	if wm, ok := s.worldMaps.getFresh(world, worldMapTTL); ok {
		return wm, nil
	}
	return s.WorldMap(ctx, world)
}

// worldClusters groups catalog docs by their top-level path segment (the
// directory), preserving the catalog's importance order within each cluster.
// The root cluster (docs directly under "/") sorts first, then directories
// alphabetically. Each cluster keeps its top perCluster docs as labeled nodes;
// the remainder becomes the More count behind the dir's listing pane.
func worldClusters(docs []domain.FloorDoc, perCluster int) []domain.WorldCluster {
	order := []string{}
	byDir := map[string][]domain.FloorDoc{}
	for _, d := range docs {
		dir := topDir(d.Path)
		if _, seen := byDir[dir]; !seen {
			order = append(order, dir)
		}
		byDir[dir] = append(byDir[dir], d)
	}
	sort.SliceStable(order, func(i, j int) bool {
		if (order[i] == "") != (order[j] == "") {
			return order[i] == "" // root cluster first
		}
		return order[i] < order[j]
	})

	out := make([]domain.WorldCluster, 0, len(order))
	for _, dir := range order {
		group := byDir[dir]
		cl := domain.WorldCluster{Dir: dir, ListPath: "/"}
		if dir != "" {
			cl.ListPath = "/" + dir + "/"
		}
		if len(group) > perCluster {
			cl.Docs = group[:perCluster]
			cl.More = len(group) - perCluster
		} else {
			cl.Docs = group
		}
		out = append(out, cl)
	}
	return out
}

// topDir returns a path's top-level directory segment — "plans" for
// "/plans/reading-room.md", "" for a root-level doc like "/index.md".
func topDir(path string) string {
	p := strings.TrimPrefix(path, "/")
	if dir, _, found := strings.Cut(p, "/"); found {
		return dir
	}
	return ""
}

// intraWorldEdges keeps the document-level edges whose endpoints both belong to
// world AND are both rendered (labeled), remapping each endpoint to the world's
// own name. Hub edges are keyed by host, observed edges by world name; host2name
// joins the former. Deduped and sorted for a stable, cacheable render.
func intraWorldEdges(world string, host2name map[string]string, all []domain.Edge, labeled map[string]bool) []domain.Edge {
	seen := map[domain.Edge]struct{}{}
	var out []domain.Edge
	for _, e := range all {
		if !worldMember(e.From.World, world, host2name) || !worldMember(e.To.World, world, host2name) {
			continue
		}
		if e.From.Path == e.To.Path || !labeled[e.From.Path] || !labeled[e.To.Path] {
			continue
		}
		ce := domain.Edge{
			From: domain.Ref{World: world, Path: e.From.Path},
			To:   domain.Ref{World: world, Path: e.To.Path},
		}
		if _, dup := seen[ce]; dup {
			continue
		}
		seen[ce] = struct{}{}
		out = append(out, ce)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].From.Path != out[j].From.Path {
			return out[i].From.Path < out[j].From.Path
		}
		return out[i].To.Path < out[j].To.Path
	})
	return out
}

// worldMember reports whether a topology ref's world resolves to world — either
// it already is the world name (observed map) or its host joins to it (hub
// graph, via host2name).
func worldMember(refWorld, world string, host2name map[string]string) bool {
	return refWorld == world || host2name[strings.ToLower(refWorld)] == world
}
