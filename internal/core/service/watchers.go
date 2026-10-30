package service

import (
	"context"
	"sync"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// documentWatchers fans change hints out to the live streams: a document, a
// listing, the pulse. Each channel holds one pending signal, so a burst costs
// one re-read; a watcher lives exactly as long as its ctx.
type documentWatchers struct {
	mu  sync.Mutex
	all map[*documentWatcher]struct{}
}

type documentWatcher struct {
	wants  func(domain.Change) bool
	signal chan struct{}
}

func (w *documentWatchers) add(ctx context.Context, wants func(domain.Change) bool) <-chan struct{} {
	watcher := &documentWatcher{wants: wants, signal: make(chan struct{}, 1)}
	w.mu.Lock()
	if w.all == nil {
		w.all = make(map[*documentWatcher]struct{})
	}
	w.all[watcher] = struct{}{}
	w.mu.Unlock()
	context.AfterFunc(ctx, func() { w.remove(watcher) })
	return watcher.signal
}

// remove closes under mu, so notify never sends on a closed channel.
func (w *documentWatchers) remove(watcher *documentWatcher) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.all, watcher)
	close(watcher.signal)
}

func (w *documentWatchers) notify(change domain.Change) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for watcher := range w.all {
		if !watcher.wants(change) {
			continue
		}
		select {
		case watcher.signal <- struct{}{}:
		default: // a signal is already pending; one re-read covers both
		}
	}
}

// showing wants a change that can show at path in world: a resync of the
// world, or a write that shows there.
func showing(world, path string) func(domain.Change) bool {
	world = worldKey(world)
	return func(change domain.Change) bool {
		return worldKey(change.World) == world && (change.Resync || shows(path, change.Path))
	}
}
