package service

import (
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
func (s *ReadingService) Backlinks(world, path string) []domain.Ref {
	return s.graph.backlinks(domain.Ref{World: world, Path: path})
}

// Neighborhood assembles the graph pane's data: the document plus its observed
// outbound (Out) and inbound (In) edges. Store-only — no world reads — so it
// renders cold in both transports (sparse until the documents are read).
func (s *ReadingService) Neighborhood(world, path string) domain.Neighborhood {
	center := domain.Ref{World: world, Path: path}
	return domain.Neighborhood{
		Center: center,
		Out:    s.graph.links(center),
		In:     s.graph.backlinks(center),
	}
}

// linkGraph is the render-time observed-links map (R3 decision; ADR 0005 §16):
// the edge source for backlinks and the graph pane. It is populated as a
// side-effect of normal reading — rewriteLinks already resolves every link in
// every rendered document to a (world, path) target, and the web adapter feeds
// those edges here via RecordLinks. Pure in-memory state, per-pod and
// ephemeral, identical under QUIC and broker (no broker graph store, so
// decision 16 holds). It is the always-available baseline; the durable
// hub-sourced topology (plans addendum 2) layers in front of it later.
//
// Mirrors floorCache: an internal struct, not a port — the service's public
// edge methods are the seam a future hub/broker strategy slots behind.
type linkGraph struct {
	mu  sync.RWMutex
	out map[domain.Ref]map[domain.Ref]struct{} // source → targets it links to
	in  map[domain.Ref]map[domain.Ref]struct{} // target → sources that link to it
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
		return
	}
	g.out[src] = set
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
			out = append(out, domain.Edge{From: src, To: to})
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
