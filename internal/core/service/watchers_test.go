package service

import (
	"context"
	"testing"
	"time"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

func pending(signal <-chan struct{}) bool {
	select {
	case _, open := <-signal:
		return open
	default:
		return false
	}
}

// A watcher is signalled for its document under any spelling of the world, and
// for a resync of that world; never for a sibling or another world.
func TestDocumentWatchersNotify(t *testing.T) {
	var w documentWatchers
	ctx := t.Context()
	tests := []struct {
		name   string
		change domain.Change
		want   bool
	}{
		{"same document", domain.Change{World: "h.example.org", Path: "/a.md"}, true},
		{"other spelling", domain.Change{World: "h.example.org:6309", Path: "/a.md"}, true},
		{"resync", domain.Change{World: "h.example.org:6309", Resync: true}, true},
		{"sibling", domain.Change{World: "h.example.org", Path: "/b.md"}, false},
		{"other world", domain.Change{World: "other.example.org", Path: "/a.md"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			signal := w.add(ctx, showing("h.example.org:6309", "/a.md"))
			w.notify(tt.change)
			if got := pending(signal); got != tt.want {
				t.Errorf("signalled = %v, want %v", got, tt.want)
			}
		})
	}
}

// A listing's watcher is signalled for anything under it, at any depth; never
// for a document beside it.
func TestDocumentWatchersNotifyAListing(t *testing.T) {
	var w documentWatchers
	tests := []struct {
		path string
		want bool
	}{{"/plans/a.md", true}, {"/plans/old/b.md", true}, {"/plansx.md", false}, {"/notes/a.md", false}}
	for _, tt := range tests {
		signal := w.add(t.Context(), showing("h.example.org", "/plans/"))
		w.notify(domain.Change{World: "h.example.org", Path: tt.path})
		if got := pending(signal); got != tt.want {
			t.Errorf("%s: signalled = %v, want %v", tt.path, got, tt.want)
		}
	}
}

// A burst of writes leaves one pending signal: one re-read, not one per write.
func TestDocumentWatchersCoalesce(t *testing.T) {
	var w documentWatchers
	signal := w.add(t.Context(), showing("h.example.org", "/a.md"))
	for range 5 {
		w.notify(domain.Change{World: "h.example.org", Path: "/a.md"})
	}
	if !pending(signal) {
		t.Fatal("no signal after a burst")
	}
	if pending(signal) {
		t.Error("a burst left more than one signal")
	}
}

// Watchers die with their ctx: the channel closes and nothing is retained.
func TestDocumentWatchersRemovedWithContext(t *testing.T) {
	var w documentWatchers
	for range 1000 {
		ctx, cancel := context.WithCancel(t.Context())
		w.add(ctx, showing("h.example.org", "/a.md"))
		cancel()
	}
	ctx, cancel := context.WithCancel(t.Context())
	signal := w.add(ctx, showing("h.example.org", "/a.md"))
	cancel()
	select {
	case _, open := <-signal:
		if open {
			t.Error("got a signal instead of a close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel still open after ctx ended")
	}
	// AfterFunc removals run on their own goroutines; give them a moment.
	deadline := time.Now().Add(2 * time.Second)
	for {
		w.mu.Lock()
		left := len(w.all)
		w.mu.Unlock()
		if left == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d watchers retained after every ctx ended", left)
		}
		time.Sleep(time.Millisecond)
	}
}
