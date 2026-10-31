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
		event(4, protocol.OpPublish, "/graph/worlds/team-a/9.md"),
		event(5, protocol.OpPublish, "/graph.md"),
	}}}}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	got := receive(t, newTestFeed(o).Changes(ctx), 6)

	want := []domain.Change{
		{World: "h.example.org:6309", Resync: true}, // the first watch has no cursor
		{World: "h.example.org:6309", Path: "/a.md", Op: domain.OpPublish, Version: 4, Agent: "claude-code", User: "ana@example.org"},
		{World: "h.example.org:6309", Resync: true},
		{World: "h.example.org:6309", Path: "/c.md", Op: domain.OpArchive},
		{World: "h.example.org:6309", Path: "/graph/worlds/team-a/9.md", Op: domain.OpPublish, Derived: true},
		{World: "h.example.org:6309", Path: "/graph.md", Op: domain.OpPublish, Derived: true},
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

	got := receive(t, newTestFeed(o).Changes(ctx), 3)

	if !got[0].Resync || got[1].Path != "/a.md" || got[2].Path != "/b.md" {
		t.Fatalf("changes = %+v, want the first watch's resync, /a.md, /b.md", got)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	resume := protocol.Cursor{Epoch: "e", Seq: 7}
	if len(o.sinces) < 3 || !o.sinces[0].IsZero() || o.sinces[1] != resume || o.sinces[2] != resume {
		t.Errorf("subscribe cursors = %v, want [zero %v %v]", o.sinces, resume, resume)
	}
}

// The channel closes once ctx ends; the first watch's resync may race the
// cancel, so it is drained.
func TestFeedClosesWhenContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	changes := newTestFeed(&opener{}).Changes(ctx)
	cancel()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, open := <-changes:
			if !open {
				return
			}
		case <-deadline:
			t.Fatal("channel still open after ctx ended")
		}
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

	if got := receive(t, f.Changes(ctx), 2); got[1].Path != "/a.md" {
		t.Errorf("changes = %+v", got)
	}
}

// A watch opened without a cursor cannot replay what landed before it, so it
// starts with a resync: a quiet world's MCP watch reopens cursorless after a
// pause. A reopen from a cursor, here an acknowledgement's, does not.
func TestFeedResyncsWithoutACursor(t *testing.T) {
	lapsed := fmt.Errorf("lend: %w", bearer.ErrNoReader)
	acked := protocol.Cursor{Epoch: "e", Seq: 3}
	o := &opener{script: []subscribeResult{
		{watch: &scriptedWatch{end: lapsed}},
		{watch: &scriptedWatch{cursor: acked, end: lapsed}},
		{watch: &scriptedWatch{notices: []fetch.Notice{event(4, protocol.OpPublish, "/a.md")}}},
	}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	got := receive(t, newTestFeed(o).Changes(ctx), 3)

	if !got[0].Resync || !got[1].Resync || got[2].Path != "/a.md" {
		t.Errorf("changes = %+v, want resync, resync, /a.md", got)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.sinces) < 3 || !o.sinces[1].IsZero() || o.sinces[2] != acked {
		t.Errorf("subscribe cursors = %v, want [zero zero %v]", o.sinces, acked)
	}
}
