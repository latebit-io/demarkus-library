package watch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/latebit-io/demarkus-library/internal/adapter/bearer"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus-library/internal/core/port"
)

// worldFeed sends one hint for its world, then holds until its ctx ends,
// recording that it stopped.
type worldFeed struct {
	world   string
	stopped chan string
}

func (f worldFeed) Changes(ctx context.Context) <-chan domain.Change {
	out := make(chan domain.Change)
	go func() {
		defer close(out)
		select {
		case out <- domain.Change{World: f.world, Path: "/x.md"}:
		case <-ctx.Done():
		}
		<-ctx.Done()
		f.stopped <- f.world
	}()
	return out
}

// lister answers each list from its script in order, repeating the last.
type lister struct {
	mu     sync.Mutex
	script []listResult
	calls  int
}

type listResult struct {
	names []string
	err   error
}

func (l *lister) list(context.Context) ([]string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	result := l.script[min(l.calls, len(l.script)-1)]
	l.calls++
	return result.names, result.err
}

func newTestWorlds(l *lister, stopped chan string) *Worlds {
	return &Worlds{
		list:   l.list,
		feed:   func(world string) port.ChangeFeed { return worldFeed{world: world, stopped: stopped} },
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		relist: 5 * time.Millisecond, retryMin: time.Millisecond, retryMax: 4 * time.Millisecond, roomPoll: time.Millisecond,
	}
}

// A failed list is retried; then every listed world gets one feed, and their
// hints arrive merged on one channel.
func TestWorldsMergesOneFeedPerListedWorld(t *testing.T) {
	l := &lister{script: []listResult{
		{err: errors.New("broker: mark_worlds: timeout")},
		{names: []string{"root", "team-a"}},
	}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	got := receive(t, newTestWorlds(l, make(chan string, 4)).Changes(ctx), 2)

	worlds := []string{got[0].World, got[1].World}
	slices.Sort(worlds)
	if !slices.Equal(worlds, []string{"root", "team-a"}) {
		t.Errorf("hints from %v, want one each from root and team-a", worlds)
	}
}

// A world dropped from the list has its feed stopped; one that stays keeps
// its feed rather than restarting it.
func TestWorldsStopsTheFeedOfAWorldGone(t *testing.T) {
	l := &lister{script: []listResult{{names: []string{"root", "team-a"}}, {names: []string{"root"}}}}
	stopped := make(chan string, 4)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	changes := newTestWorlds(l, stopped).Changes(ctx)
	receive(t, changes, 2)

	select {
	case world := <-stopped:
		if world != "team-a" {
			t.Errorf("stopped %q, want team-a", world)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("feed of a world gone still running")
	}
	select {
	case change := <-changes:
		t.Errorf("a feed restarted: %+v", change)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestWorldsClosesAfterEveryFeedStops(t *testing.T) {
	l := &lister{script: []listResult{{names: []string{"root"}}}}
	ctx, cancel := context.WithCancel(t.Context())
	changes := newTestWorlds(l, make(chan string, 1)).Changes(ctx)
	receive(t, changes, 1)
	cancel()
	select {
	case _, open := <-changes:
		if open {
			t.Error("got a change after ctx ended")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel still open after ctx ended")
	}
}

// An empty room is polled, not backed off: the first reader's worlds start
// within a poll even when a fault's backoff would hold them for minutes.
func TestWorldsStartOnTheFirstReaderWithoutBackoff(t *testing.T) {
	empty := listResult{err: fmt.Errorf("lend: %w", bearer.ErrNoReader)}
	l := &lister{script: []listResult{empty, empty, empty, empty, empty, {names: []string{"root"}}}}
	w := newTestWorlds(l, make(chan string, 1))
	w.retryMin, w.retryMax = time.Hour, time.Hour
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	got := receive(t, w.Changes(ctx), 1)

	if got[0].World != "root" {
		t.Errorf("hint from %q, want root", got[0].World)
	}
}
