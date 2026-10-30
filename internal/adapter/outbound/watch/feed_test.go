package watch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/latebit-io/demarkus-library/internal/adapter/bearer"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus/client/fetch"
	"github.com/latebit-io/demarkus/protocol"
)

// scriptedWatch serves its notices, then ends with end, or holds until ctx
// ends when end is nil. Cursor reports the last notice Next returned.
type scriptedWatch struct {
	notices []fetch.Notice
	end     error
	cursor  protocol.Cursor
}

func (w *scriptedWatch) Next(ctx context.Context) (fetch.Notice, error) {
	if len(w.notices) > 0 {
		notice := w.notices[0]
		w.notices = w.notices[1:]
		w.cursor = notice.Event.Cursor
		return notice, nil
	}
	if w.end != nil {
		return fetch.Notice{}, w.end
	}
	<-ctx.Done()
	return fetch.Notice{}, ctx.Err()
}

func (w *scriptedWatch) Cursor() protocol.Cursor { return w.cursor }
func (w *scriptedWatch) Close()                  {}

// subscribeResult is one scripted answer to a subscribe.
type subscribeResult struct {
	watch *scriptedWatch
	err   error
}

// opener answers each subscribe from its script in order. It records the
// cursor each subscribe sent.
type opener struct {
	mu      sync.Mutex
	script  []subscribeResult
	sinces  []protocol.Cursor
	attempt int
}

func (o *opener) open(_ context.Context, since protocol.Cursor) (noticeSource, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sinces = append(o.sinces, since)
	i := o.attempt
	o.attempt++
	if i >= len(o.script) {
		return &scriptedWatch{}, nil // nothing more scripted: hold open
	}
	result := o.script[i]
	if result.err != nil {
		return nil, result.err
	}
	return result.watch, nil
}

func newTestFeed(o *opener) *Feed {
	return &Feed{
		open: o.open, world: "h.example.org:6309",
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		retryMin: time.Millisecond, retryMax: 4 * time.Millisecond, roomPoll: time.Millisecond,
	}
}

func event(seq uint64, op, path string) fetch.Notice {
	return fetch.Notice{Event: protocol.WatchEvent{Cursor: protocol.Cursor{Epoch: "e", Seq: seq}, Op: op, Path: path}}
}

func receive(t *testing.T, changes <-chan domain.Change, n int) []domain.Change {
	t.Helper()
	var got []domain.Change
	for range n {
		select {
		case change := <-changes:
			got = append(got, change)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out after %d of %d changes: %+v", len(got), n, got)
		}
	}
	return got
}

// Events and resyncs become hints stamped with the world, carrying who wrote
// what; an unknown op is skipped, not fatal.
func TestFeedMapsNotices(t *testing.T) {
	published := event(1, protocol.OpPublish, "/a.md")
	published.Event.Version, published.Event.Agent, published.Event.User = 4, "claude-code", "ana@example.org"
	o := &opener{script: []subscribeResult{{watch: &scriptedWatch{notices: []fetch.Notice{
		published,
		event(2, "rename", "/b.md"),
		{Resync: true},
		event(3, protocol.OpArchive, "/c.md"),
	}}}}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	got := receive(t, newTestFeed(o).Changes(ctx), 3)

	want := []domain.Change{
		{World: "h.example.org:6309", Path: "/a.md", Op: domain.OpPublish, Version: 4, Agent: "claude-code", User: "ana@example.org"},
		{World: "h.example.org:6309", Resync: true},
		{World: "h.example.org:6309", Path: "/c.md", Op: domain.OpArchive},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("change %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// A watch the server ended, and a failed subscribe, are retried; every reopen
// resumes from the last cursor read so the server can replay or resync.
func TestFeedReopensFromLastCursor(t *testing.T) {
	o := &opener{script: []subscribeResult{
		{watch: &scriptedWatch{notices: []fetch.Notice{event(7, protocol.OpPublish, "/a.md")}, end: &fetch.StatusError{Status: "unauthorized"}}},
		{err: errors.New("dial: connection refused")},
		{watch: &scriptedWatch{notices: []fetch.Notice{event(8, protocol.OpAppend, "/b.md")}}},
	}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	got := receive(t, newTestFeed(o).Changes(ctx), 2)

	if got[0].Path != "/a.md" || got[1].Path != "/b.md" {
		t.Fatalf("changes = %+v", got)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	resume := protocol.Cursor{Epoch: "e", Seq: 7}
	if len(o.sinces) < 3 || !o.sinces[0].IsZero() || o.sinces[1] != resume || o.sinces[2] != resume {
		t.Errorf("subscribe cursors = %v, want [zero %v %v]", o.sinces, resume, resume)
	}
}

func TestFeedClosesWhenContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	changes := newTestFeed(&opener{}).Changes(ctx)
	cancel()
	select {
	case _, open := <-changes:
		if open {
			t.Error("got a change from an empty feed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel still open after ctx ended")
	}
}

// A feed whose source has no reader polls for one rather than backing off.
func TestFeedPollsAnEmptyRoom(t *testing.T) {
	empty := subscribeResult{err: fmt.Errorf("lend: %w", bearer.ErrNoReader)}
	o := &opener{script: []subscribeResult{empty, empty, empty, empty,
		{watch: &scriptedWatch{notices: []fetch.Notice{event(1, protocol.OpPublish, "/a.md")}}}}}
	f := newTestFeed(o)
	f.retryMin, f.retryMax = time.Hour, time.Hour
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	if got := receive(t, f.Changes(ctx), 1); got[0].Path != "/a.md" {
		t.Errorf("change = %+v", got[0])
	}
}
