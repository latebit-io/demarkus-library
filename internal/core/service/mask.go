package service

import (
	"context"
	"fmt"
	"slices"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// readerHosts is the reader's world list and its index, cached per reader for
// hubTTL so the floor, world map, hub join and observed-graph mask share one
// broker read. A failed list is never cached.
func (s *ReadingService) readerHosts(ctx context.Context) (hostIndex, error) {
	if idx, ok := s.hosts.getFresh(ctx, universe, hubTTL); ok {
		return idx, nil
	}
	epoch := s.hosts.epoch(universe)
	worlds, err := s.world.Worlds(ctx)
	if err != nil {
		return hostIndex{}, fmt.Errorf("list worlds: %w", err)
	}
	idx := indexHosts(worlds)
	s.hosts.put(ctx, universe, epoch, idx)
	return idx, nil
}

// visible reports whether the reader may see world: a knowledge-system name
// only when their world list carries it; a host-shaped id is the open network,
// readable by anyone, so it always shows.
func (h hostIndex) visible(world string) bool {
	return h.authorized[world] || domain.IsHostShaped(world)
}

// visibleRefs drops the refs in worlds the reader may not see; nil when none
// remain, like sortedRefs.
func (h hostIndex) visibleRefs(refs []domain.Ref) []domain.Ref {
	refs = slices.DeleteFunc(refs, func(r domain.Ref) bool { return !h.visible(r.World) })
	if len(refs) == 0 {
		return nil
	}
	return refs
}

// observedLinks is the render-time link map every reader fills and shares.
// Each read takes the reader's world index, so no consumer can skip the mask:
// an edge shows only when both of its worlds are visible.
type observedLinks struct{ g linkGraph }

func (o *observedLinks) observe(src domain.Ref, targets []domain.Ref) { o.g.observe(src, targets) }

// links returns src's visible targets; none when src itself is hidden.
func (o *observedLinks) links(h hostIndex, src domain.Ref) []domain.Ref {
	if !h.visible(src.World) {
		return nil
	}
	return h.visibleRefs(o.g.links(src))
}

// backlinks returns target's visible sources; none when target is hidden.
func (o *observedLinks) backlinks(h hostIndex, target domain.Ref) []domain.Ref {
	if !h.visible(target.World) {
		return nil
	}
	return h.visibleRefs(o.g.backlinks(target))
}

// edges returns every visible forward edge: one into a hidden world would
// name it, as a portal on the floor.
func (o *observedLinks) edges(h hostIndex) []domain.Edge {
	return slices.DeleteFunc(o.g.allEdges(), func(e domain.Edge) bool {
		return !h.visible(e.From.World) || !h.visible(e.To.World)
	})
}
