package service

import (
	"context"
	"sort"
	"sync"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// RecordLinks stores the in-universe document links observed in the rendered
// document at (world, path). Called by the web adapter after it resolves the
// document's links (ADR 0005 §11/§16): resolution stays in the adapter that
// owns the URL scheme, the edge store stays in the core.
func (s *ReadingService) RecordLinks(world, path string, targets []domain.Ref) {
	s.graph.observe(domain.Ref{World: world, Path: path}, targets)
}

// Backlinks returns the documents observed linking to (world, path).
func (s *ReadingService) Backlinks(ctx context.Context, world, path string) []domain.Ref {
	return s.Neighborhood(ctx, world, path).In
}

// Neighborhood assembles the graph pane's data: the document plus its
// outbound (Out) and inbound (In) edges, the observed map unioned with the
// hub's durable edges so a cold pod still draws what the hub knows.
func (s *ReadingService) Neighborhood(ctx context.Context, world, path string) domain.Neighborhood {
	center := domain.Ref{World: world, Path: path}
	// A failed world list hides every knowledge-system name (fail closed: the
	// pane is enrichment) and resolves no hub edge, so the hub read is skipped.
	hosts, err := s.readerHosts(ctx)
	durable := noHubEdges
	if err == nil {
		durable = s.hubGraph(ctx).edges
	}
	return domain.Neighborhood{
		Center: center,
		Out:    unionRefs(s.graph.links(hosts, center), durable.links(center)),
		In:     unionRefs(s.graph.backlinks(hosts, center), durable.backlinks(center)),
	}
}

// unionRefs merges two sorted ref lists, each once, sorted; nil when both are empty.
func unionRefs(a, b []domain.Ref) []domain.Ref {
	if len(b) == 0 {
		return a
	}
	if len(a) == 0 {
		return b
	}
	set := make(map[domain.Ref]struct{}, len(a)+len(b))
	for _, r := range a {
		set[r] = struct{}{}
	}
	for _, r := range b {
		set[r] = struct{}{}
	}
	return sortedRefs(set)
}

// maxLinkGraphSources bounds the observed-links map so a long-lived pod does not
// grow without limit as distinct documents are read. When exceeded, the
// least-recently-observed source (and its edges) is evicted; the graph is
// best-effort enrichment, so dropping cold edges is harmless — they re-record
// when that document is next read.
const maxLinkGraphSources = 5000

// linkGraph holds source→target edges. observedLinks wraps the render-time
// observed map (R3; ADR 0005 §16): per pod, ephemeral, capped. hubGraph keeps an
// unbounded one filled from the durable hub topology.
type linkGraph struct {
	unbounded bool // no source cap: the hub's durable copy must stay whole
	mu        sync.RWMutex
	out       map[domain.Ref]map[domain.Ref]struct{} // source → targets it links to
	in        map[domain.Ref]map[domain.Ref]struct{} // target → sources that link to it
	seq       map[domain.Ref]uint64                  // last-observed tick per source, for LRU eviction
	tick      uint64
}

// observe records that src links to exactly targets, replacing any previously
// observed edge set for src — a re-render is fresh truth, so a link removed
// from the document must vanish from the graph (including its reverse entry).
func (g *linkGraph) observe(src domain.Ref, targets []domain.Ref) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.out == nil {
		g.out = make(map[domain.Ref]map[domain.Ref]struct{})
		g.in = make(map[domain.Ref]map[domain.Ref]struct{})
		g.seq = make(map[domain.Ref]uint64)
	}
	// Drop src's old forward edges from the reverse index before rebuilding,
	// so stale backlinks don't survive an edit.
	for old := range g.out[src] {
		delete(g.in[old], src)
		if len(g.in[old]) == 0 {
			delete(g.in, old)
		}
	}
	set := make(map[domain.Ref]struct{}, len(targets))
	for _, t := range targets {
		if t == src {
			continue // no self-loops
		}
		set[t] = struct{}{}
		if g.in[t] == nil {
			g.in[t] = make(map[domain.Ref]struct{})
		}
		g.in[t][src] = struct{}{}
	}
	if len(set) == 0 {
		delete(g.out, src)
		delete(g.seq, src)
		return
	}
	g.out[src] = set
	g.tick++
	g.seq[src] = g.tick
	g.evictLocked()
}

// observeAll records every source's edge set at once — how the hub's
// resolved topology fills a fresh store.
func (g *linkGraph) observeAll(bySource map[domain.Ref][]domain.Ref) {
	for src, targets := range bySource {
		g.observe(src, targets)
	}
}

// evictLocked bounds the source count, dropping the least-recently-observed
// source (and its edges) until within maxLinkGraphSources. Called under g.mu.
// Re-observing an existing source replaces its edge set without growing the
// map, so eviction only fires on genuinely new sources past the cap.
func (g *linkGraph) evictLocked() {
	for !g.unbounded && len(g.out) > maxLinkGraphSources {
		var victim domain.Ref
		var minSeq uint64
		first := true
		for src, sq := range g.seq {
			if first || sq < minSeq {
				minSeq, victim, first = sq, src, false
			}
		}
		if first {
			return // seq empty — nothing to evict
		}
		for old := range g.out[victim] {
			delete(g.in[old], victim)
			if len(g.in[old]) == 0 {
				delete(g.in, old)
			}
		}
		delete(g.out, victim)
		delete(g.seq, victim)
	}
}

// backlinks returns the sources observed linking to target, sorted for a
// stable (cacheable, diffable) render. Empty when nothing has linked to it
// yet — the correct cold state (ADR 0005 decision 8).
func (g *linkGraph) backlinks(target domain.Ref) []domain.Ref {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return sortedRefs(g.in[target])
}

// links returns the targets src was observed linking to (its forward edges).
func (g *linkGraph) links(src domain.Ref) []domain.Ref {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return sortedRefs(g.out[src])
}

// allEdges returns every observed forward edge — the floor unions these with
// the durable hub graph so edges show even before (or without) a hub.
func (g *linkGraph) allEdges() []domain.Edge {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var out []domain.Edge
	for src, tos := range g.out {
		for to := range tos {
			out = append(out, domain.Edge{From: src, To: to, Type: domain.EdgeReference})
		}
	}
	return out
}

// sortedRefs flattens a ref set into a deterministically ordered slice.
func sortedRefs(set map[domain.Ref]struct{}) []domain.Ref {
	if len(set) == 0 {
		return nil
	}
	out := make([]domain.Ref, 0, len(set))
	for r := range set {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].World != out[j].World {
			return out[i].World < out[j].World
		}
		return out[i].Path < out[j].Path
	})
	return out
}
