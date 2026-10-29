package watch

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus-library/internal/core/port"
)

// relistEvery is how often Worlds asks which worlds exist; worlds come and
// go rarely, and a feed already rides out everything else.
const relistEvery = 5 * time.Minute

// Worlds runs one feed per world that list names, merged into one change
// feed, and relists to follow worlds coming and going. Implements
// port.ChangeFeed for broker mode, where the pod has no world list of its own.
type Worlds struct {
	list     func(ctx context.Context) ([]string, error)
	feed     func(world string) port.ChangeFeed
	logger   *slog.Logger
	relist   time.Duration
	retryMin time.Duration
	retryMax time.Duration
	roomPoll time.Duration
}

var _ port.ChangeFeed = (*Worlds)(nil)

// NewWorlds follows every world list names, each through the feed feed builds.
func NewWorlds(list func(ctx context.Context) ([]string, error), feed func(world string) port.ChangeFeed, logger *slog.Logger) *Worlds {
	return &Worlds{list: list, feed: feed, logger: logger, relist: relistEvery, retryMin: retryMin, retryMax: retryMax, roomPoll: roomPoll}
}

// worldRun is one Changes call's state: the feeds running, per world, and
// where their hints merge.
type worldRun struct {
	running map[string]context.CancelFunc
	out     chan<- domain.Change
	feeds   sync.WaitGroup
}

// Changes merges every world's hints; the channel closes when ctx ends and
// every feed has stopped.
func (w *Worlds) Changes(ctx context.Context) <-chan domain.Change {
	out := make(chan domain.Change)
	go func() {
		defer close(out)
		r := &worldRun{running: map[string]context.CancelFunc{}, out: out}
		defer r.feeds.Wait()
		w.run(ctx, r)
	}()
	return out
}

// run relists until ctx ends. A failed list retries as the feeds do and
// leaves the running feeds alone; an empty room cannot list at all.
func (w *Worlds) run(ctx context.Context, r *worldRun) {
	pace := newPacer(w.retryMin, w.retryMax, w.roomPoll)
	for {
		wait := w.relist
		names, err := w.list(ctx)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			var report bool
			wait, report = pace.next(err)
			if report {
				w.logger.Log(ctx, retryLevel(err), "watch: listing worlds", "retry_in", wait, "err", err)
			}
		default:
			pace.reset()
			w.reconcile(ctx, names, r)
		}
		if !sleep(ctx, wait) {
			return
		}
	}
}

// reconcile starts a feed for each newly listed world and stops the feed of
// each world no longer listed.
func (w *Worlds) reconcile(ctx context.Context, names []string, r *worldRun) {
	listed := make(map[string]bool, len(names))
	for _, name := range names {
		listed[name] = true
		if r.running[name] != nil {
			continue
		}
		feedCtx, stop := context.WithCancel(ctx)
		r.running[name] = stop
		changes := w.feed(name).Changes(feedCtx)
		r.feeds.Go(func() { forward(feedCtx, changes, r.out) })
	}
	for name, stop := range r.running {
		if !listed[name] {
			w.logger.Info("watch: world gone", "world", name)
			stop()
			delete(r.running, name)
		}
	}
}

// forward copies one feed's hints until it closes or ctx ends.
func forward(ctx context.Context, changes <-chan domain.Change, out chan<- domain.Change) {
	for change := range changes {
		select {
		case out <- change:
		case <-ctx.Done():
			return
		}
	}
}
