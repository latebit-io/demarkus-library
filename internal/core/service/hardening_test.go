package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// TestWorldMapCacheTTLAndInvalidate covers the cache primitive directly: a put
// is fresh within the TTL, stale at/after it, and invalidate drops it outright.
func TestWorldMapCacheTTLAndInvalidate(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	c := worldMapCache{now: func() time.Time { return now }}
	wm := domain.WorldMap{World: domain.WorldInfo{Name: "w"}}

	c.put("w", wm)
	if _, ok := c.getFresh("w", worldMapTTL); !ok {
		t.Fatal("a fresh put must hit")
	}
	now = now.Add(worldMapTTL) // exactly TTL ⇒ stale (>=)
	if _, ok := c.getFresh("w", worldMapTTL); ok {
		t.Fatal("an entry at the TTL boundary must be stale")
	}

	c.put("w", wm) // re-prime at the advanced clock
	if _, ok := c.getFresh("w", worldMapTTL); !ok {
		t.Fatal("re-put must be fresh again")
	}
	c.invalidate("w")
	if _, ok := c.getFresh("w", worldMapTTL); ok {
		t.Fatal("invalidate must drop the entry")
	}
}

// TestWriteInvalidatesTopology: a clean Publish/Append drops the cached floor and
// the world's cached map so the just-written document shows immediately — but a
// merge candidate (nothing written) leaves the caches intact.
func TestWriteInvalidatesTopology(t *testing.T) {
	raw := domain.RawDocument{Path: "/p.md", Body: "x", Metadata: map[string]string{"version": "2"}}

	prime := func(s *ReadingService) {
		s.floor.put(domain.Floor{Worlds: []domain.FloorWorld{{World: domain.WorldInfo{Name: "w"}}}})
		s.worldMaps.put("w", domain.WorldMap{World: domain.WorldInfo{Name: "w"}})
	}
	cachesCleared := func(t *testing.T, s *ReadingService) {
		t.Helper()
		if _, ok := s.floor.get(); ok {
			t.Error("floor cache not invalidated on write")
		}
		if _, ok := s.worldMaps.getFresh("w", worldMapTTL); ok {
			t.Error("world map cache not invalidated on write")
		}
	}

	t.Run("publish", func(t *testing.T) {
		s := NewReadingService(fakeGateway{raw: raw, publishVersion: 2}, fakeRenderer{html: "x"}, nil)
		prime(s)
		if _, merge, err := s.Publish(t.Context(), "w", "/p.md", "x", domain.PublishMeta{}, 1); err != nil || merge != nil {
			t.Fatalf("Publish: merge=%v err=%v", merge, err)
		}
		cachesCleared(t, s)
	})

	t.Run("append", func(t *testing.T) {
		s := NewReadingService(fakeGateway{raw: raw, publishVersion: 3}, fakeRenderer{html: "x"}, nil)
		prime(s)
		if _, err := s.Append(t.Context(), "w", "/p.md", "more"); err != nil {
			t.Fatalf("Append: %v", err)
		}
		cachesCleared(t, s)
	})

	t.Run("merge candidate leaves caches", func(t *testing.T) {
		gw := fakeGateway{raw: raw, publishMerge: &domain.MergeCandidate{Body: "merged", PublishAtVersion: 5}}
		s := NewReadingService(gw, fakeRenderer{html: "x"}, nil)
		prime(s)
		_, merge, err := s.Publish(t.Context(), "w", "/p.md", "x", domain.PublishMeta{}, 1)
		if err != nil || merge == nil {
			t.Fatalf("expected a merge candidate: merge=%v err=%v", merge, err)
		}
		if _, ok := s.floor.get(); !ok {
			t.Error("a merge candidate (nothing written) must not invalidate the floor")
		}
	})
}

// TestLinkGraphEviction: observing past the cap drops the least-recently-observed
// sources and keeps the map bounded, while the newest sources and the reverse
// index stay consistent.
func TestLinkGraphEviction(t *testing.T) {
	var g linkGraph
	target := domain.Ref{World: "w", Path: "/t.md"}
	total := maxLinkGraphSources + 50
	for i := range total {
		g.observe(domain.Ref{World: "w", Path: fmt.Sprintf("/s%d.md", i)}, []domain.Ref{target})
	}

	g.mu.RLock()
	n := len(g.out)
	g.mu.RUnlock()
	if n > maxLinkGraphSources {
		t.Errorf("out size = %d, want <= cap %d", n, maxLinkGraphSources)
	}
	// The oldest source was evicted; the newest survives.
	if links := g.links(domain.Ref{World: "w", Path: "/s0.md"}); links != nil {
		t.Error("oldest source should have been evicted")
	}
	newest := domain.Ref{World: "w", Path: fmt.Sprintf("/s%d.md", total-1)}
	if links := g.links(newest); len(links) != 1 {
		t.Errorf("newest source links = %v, want 1", links)
	}
	// The reverse index tracks the same bounded set — no orphaned backlinks.
	if bl := g.backlinks(target); len(bl) != n {
		t.Errorf("backlinks = %d, want %d (== live source count)", len(bl), n)
	}
}
