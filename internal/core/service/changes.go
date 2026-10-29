package service

import (
	"context"
	"strings"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus-library/internal/core/port"
)

// Change hints (plans/watch-realtime.md) keep the caches honest between the
// focused-live re-reads: a write anywhere evicts what it made stale, for every
// reader, since a hint names no reader and a re-read costs less than a stale page.

// FollowChanges applies every hint from feed until ctx ends. Caches go first,
// so a watcher's re-read never lands on the stale entry.
func (s *ReadingService) FollowChanges(ctx context.Context, feed port.ChangeFeed) {
	for change := range feed.Changes(ctx) {
		s.applyChange(change)
		s.watchers.notify(change)
	}
}

// WatchDocument implements port.DocumentWatcher.
func (s *ReadingService) WatchDocument(ctx context.Context, world, path string) <-chan struct{} {
	return s.watchers.add(ctx, world, path)
}

func (s *ReadingService) applyChange(change domain.Change) {
	world := worldKey(change.World)
	if s.cache != nil {
		s.cache.Evict(func(key port.CacheKey) bool { return staleAfter(key, change, world) })
	}
	s.invalidateTopology(change)
}

// staleAfter reports whether change, in the world keyed world, can make the
// render at key stale: the document itself, a listing above it, or any tag
// page, since a hint does not say which tags moved. Pinned editions never
// change and are kept.
func staleAfter(key port.CacheKey, change domain.Change, world string) bool {
	if worldKey(key.World) != world {
		return false
	}
	if change.Resync || key.Kind != kindDoc {
		return true
	}
	return key.Name == change.Path ||
		(domain.IsListingPath(key.Name) && strings.HasPrefix(change.Path, key.Name))
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
