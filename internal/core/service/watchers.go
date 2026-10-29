package service

import (
	"context"
	"sync"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// documentWatchers fans change hints out to readers watching one document, or
// one listing and everything under it. Each signal channel holds one pending
// signal, so a burst of writes costs one re-read; a watcher lives exactly as
// long as its ctx.
type documentWatchers struct {
	mu  sync.Mutex
	all map[*documentWatcher]struct{}
}

type documentWatcher struct {
	world, path string // world as worldKey spells it
	signal      chan struct{}
}

func (w *documentWatchers) add(ctx context.Context, world, path string) <-chan struct{} {
	watcher := &documentWatcher{world: worldKey(world), path: path, signal: make(chan struct{}, 1)}
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
	world := worldKey(change.World)
	w.mu.Lock()
	defer w.mu.Unlock()
	for watcher := range w.all {
		if watcher.world != world || (!change.Resync && !shows(watcher.path, change.Path)) {
			continue
		}
		select {
		case watcher.signal <- struct{}{}:
		default: // a signal is already pending; one re-read covers both
		}
	}
}
