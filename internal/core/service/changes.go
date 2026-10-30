package service

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus-library/internal/core/port"
)

// Change hints (plans/watch-realtime.md) keep the caches honest between the
// focused-live re-reads: a write anywhere evicts what it made stale, for every
// reader, since a hint names no reader and a re-read costs less than a stale page.

// FollowChanges applies every hint from feed until ctx ends. Caches and the
// pulse go first, so a watcher's re-read never lands on the stale entry; also
// hands each hint to the adapters' own caches, which the core cannot name.
func (s *ReadingService) FollowChanges(ctx context.Context, feed port.ChangeFeed, also ...func(domain.Change)) {
	for change := range feed.Changes(ctx) {
		s.applyChange(change)
		for _, apply := range also {
			apply(change)
		}
		if !change.Resync {
			s.pulse.record(domain.Pulse{Change: change, At: time.Now()})
		}
		s.watchers.notify(change)
	}
}

// WatchDocument implements port.DocumentWatcher.
func (s *ReadingService) WatchDocument(ctx context.Context, world, path string) <-chan struct{} {
	return s.watchers.add(ctx, showing(world, path))
}

func (s *ReadingService) applyChange(change domain.Change) {
	world := worldKey(change.World)
	if s.cache != nil {
		s.fence.mu.Lock()
		s.fence.gen++
		s.cache.Evict(func(key port.CacheKey) bool { return staleAfter(key, change, world) })
		s.fence.mu.Unlock()
	}
	s.invalidateTopology(change)
}

// docFence keeps a read in flight across a hint from caching the edition the
// hint made stale (ttlCache's epochs, for the document cache). A hint holds it
// for writing across its bump and evict, a put for reading across its check.
type docFence struct {
	mu  sync.RWMutex
	gen uint64 // hints applied
}

// epoch is taken before a live read and handed to cachePut.
func (f *docFence) epoch() uint64 {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.gen
}

// staleAfter reports whether change, in the world keyed world, can make the
// render at key stale: the document itself, its pinned editions (a write's
// retention may prune them), a listing above it, or any tag page, since a
// hint does not say which tags moved. A resync evicts the whole world.
func staleAfter(key port.CacheKey, change domain.Change, world string) bool {
	if worldKey(key.World) != world {
		return false
	}
	if change.Resync || key.Kind != kindDoc {
		return true
	}
	return shows(key.Name, change.Path) ||
		(domain.IsVersionPath(key.Name) && key.Name[:strings.LastIndex(key.Name, "/")] == change.Path)
}

// shows reports whether a write to changed shows at name: the document
// itself, or a listing above it.
func shows(name, changed string) bool {
	return name == changed || (domain.IsListingPath(name) && strings.HasPrefix(changed, name))
}

// worldKey is the one spelling of a world identifier: a host gets hostKey's
// explicit port, so "h" and "h:6309" (both appear in links) match, while a
// world name is kept as is.
func worldKey(worldID string) string {
	if !domain.IsHostShaped(worldID) {
		return worldID
	}
	return hostKey(worldID)
}
