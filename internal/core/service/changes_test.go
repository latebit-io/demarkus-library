package service

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus-library/internal/core/port"
)

// A document change evicts that document, the listings above it and the
// world's tag pages, for every reader and under every spelling of the world; a
// resync evicts the whole world. Neither touches another world.
func TestApplyChangeEvictsWhatTheWriteMadeStale(t *testing.T) {
	readerA := port.WithReader(t.Context(), "a")
	readerB := port.WithReader(t.Context(), "b")
	tests := []struct {
		name        string
		change      domain.Change
		stale, kept []port.CacheKey
	}{
		{
			name:   "document change",
			change: domain.Change{World: "soul.demarkus.io:6309", Path: "/a/x.md"},
			stale: []port.CacheKey{
				docKey(readerA, "soul.demarkus.io", "/a/x.md"),
				docKey(readerB, "soul.demarkus.io:6309", "/a/x.md"),
				docKey(readerA, "soul.demarkus.io", "/a/"),
				docKey(readerA, "soul.demarkus.io", "/"),
				tagKey(readerB, "soul.demarkus.io", "plan"),
			},
			kept: []port.CacheKey{
				docKey(readerA, "soul.demarkus.io", "/a/y.md"),
				docKey(readerA, "soul.demarkus.io", "/b/"),
				docKey(readerA, "soul.demarkus.io", "/a/x.md/v2"),
				docKey(readerA, "other.example.org", "/a/x.md"),
				docKey(readerA, "soul.demarkus.io:7000", "/a/x.md"),
			},
		},
		{
			name:   "resync",
			change: domain.Change{World: "h.example.org:6309", Resync: true},
			stale: []port.CacheKey{
				docKey(readerA, "h.example.org:6309", "/a/x.md/v2"),
				docKey(readerA, "h.example.org", "/"),
				tagKey(readerA, "h.example.org", "t"),
			},
			kept: []port.CacheKey{docKey(readerA, "root", "/x.md")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newFakeCache()
			for _, key := range slices.Concat(tt.stale, tt.kept) {
				c.Put(key, domain.Document{})
			}
			svc := NewReadingService(fakeGateway{}, fakeRenderer{}, c)

			svc.applyChange(tt.change)

			for _, key := range tt.stale {
				if _, ok := c.store[key]; ok {
					t.Errorf("kept stale %+v", key)
				}
			}
			for _, key := range tt.kept {
				if _, ok := c.store[key]; !ok {
					t.Errorf("evicted %+v", key)
				}
			}
		})
	}
}

// A change also drops the floor, the world's map and name index under any
// spelling; the hub goes only when its graph export changes.
func TestApplyChangeInvalidatesTopology(t *testing.T) {
	ctx := port.WithReader(t.Context(), "a")
	svc := NewReadingService(fakeGateway{}, fakeRenderer{}, nil)
	svc.hub = "soul.demarkus.io"
	svc.floor.put(ctx, universe, svc.floor.epoch(universe), domain.Floor{})
	svc.worldMaps.put(ctx, "soul.demarkus.io", svc.worldMaps.epoch("soul.demarkus.io"), domain.WorldMap{})
	svc.worldMaps.put(ctx, "root", svc.worldMaps.epoch("root"), domain.WorldMap{})
	svc.names.put(ctx, "soul.demarkus.io", svc.names.epoch("soul.demarkus.io"), nil)
	svc.hubs.put(ctx, universe, svc.hubs.epoch(universe), hubGraph{})

	svc.applyChange(domain.Change{World: "soul.demarkus.io:6309", Path: "/x.md"})

	if _, ok := svc.floor.getFresh(ctx, universe, time.Minute); ok {
		t.Error("floor survived the change")
	}
	if _, ok := svc.worldMaps.getFresh(ctx, "soul.demarkus.io", time.Minute); ok {
		t.Error("world map survived the change")
	}
	if _, ok := svc.names.getFresh(ctx, "soul.demarkus.io", time.Minute); ok {
		t.Error("name index survived the change")
	}
	if _, ok := svc.worldMaps.getFresh(ctx, "root", time.Minute); !ok {
		t.Error("another world's map was dropped")
	}
	if _, ok := svc.hubs.getFresh(ctx, universe, time.Minute); !ok {
		t.Error("hub dropped by a change outside its graph export")
	}

	svc.applyChange(domain.Change{World: "soul.demarkus.io", Path: hubGraphPath})

	if _, ok := svc.hubs.getFresh(ctx, universe, time.Minute); ok {
		t.Error("hub survived a change to its graph export")
	}
}

func TestWorldKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b string
		want bool
	}{
		{"equal names", "root", "root", true},
		{"different names", "root", "team-a", false},
		{"host with and without port", "soul.demarkus.io", "soul.demarkus.io:6309", true},
		{"host case", "Soul.Demarkus.io:6309", "soul.demarkus.io", true},
		{"different ports, same host", "localhost:6309", "localhost:7000", false},
		{"different hosts", "a.example.org", "b.example.org", false},
		{"name never matches a host", "root", "root:6309", false},
		{"bracketed ipv6", "[::1]:6309", "::1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := worldKey(tc.a) == worldKey(tc.b); got != tc.want {
				t.Errorf("worldKey(%q) == worldKey(%q) is %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

type fakeFeed []domain.Change

func (f fakeFeed) Changes(context.Context) <-chan domain.Change {
	out := make(chan domain.Change, len(f))
	for _, change := range f {
		out <- change
	}
	close(out)
	return out
}

// FollowChanges applies every hint: each stale render goes, and a watcher of
// a changed document is signalled.
func TestFollowChangesAppliesEveryHint(t *testing.T) {
	ctx := port.WithReader(t.Context(), "a")
	c := newFakeCache()
	first, second := docKey(ctx, "h.example.org", "/1.md"), docKey(ctx, "h.example.org", "/2.md")
	c.Put(first, domain.Document{})
	c.Put(second, domain.Document{})
	svc := NewReadingService(fakeGateway{}, fakeRenderer{}, c)
	signal := svc.WatchDocument(ctx, "h.example.org", "/1.md")

	svc.FollowChanges(ctx, fakeFeed{{World: "h.example.org", Path: "/1.md"}, {World: "h.example.org", Path: "/2.md"}})

	if len(c.store) != 0 {
		t.Errorf("entries left after both hints: %d", len(c.store))
	}
	if !pending(signal) {
		t.Error("watcher not signalled")
	}
}
