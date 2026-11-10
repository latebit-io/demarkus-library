package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

func TestNameIndexWorldScope(t *testing.T) {
	gw := fakeGateway{raw: domain.RawDocument{Body: worldMapCatalog}}
	got, err := NewReadingService(gw, fakeRenderer{}, nil).NameIndex(t.Context(), "world-a")
	if err != nil {
		t.Fatalf("NameIndex: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("entries = %d, want 5 (%+v)", len(got), got)
	}
	byPath := map[string]domain.IndexEntry{}
	for _, e := range got {
		byPath[e.Path] = e
		if e.World != "world-a" {
			t.Errorf("%s world = %q, want world-a", e.Path, e.World)
		}
	}
	if e := byPath["/index.md"]; e.Title != "Home" || e.Status != "accepted" || strings.Join(e.Tags, ",") != "hub,status:accepted" {
		t.Errorf("/index.md entry = %+v, want title Home, status accepted and both tags", e)
	}
	// The index keeps the catalog's rank order: the matcher breaks ties by it.
	if got[0].Path != "/index.md" || got[4].Path != "/adr/0001.md" {
		t.Errorf("order = %s … %s, want the catalog's", got[0].Path, got[4].Path)
	}
	if e := byPath["/plans/a.md"]; e.Title != "Plan A" {
		t.Errorf("/plans/a.md title = %q, want Plan A", e.Title)
	}
}

func TestNameIndexRequestsHighLimit(t *testing.T) {
	// The switcher must not be truncated to the server's default (10): it asks
	// for the match-all cap so it reaches the whole catalog.
	var gotLimit int
	gw := fakeGateway{raw: domain.RawDocument{Body: worldMapCatalog}, limit: &gotLimit}
	if _, err := NewReadingService(gw, fakeRenderer{}, nil).NameIndex(t.Context(), "world-a"); err != nil {
		t.Fatalf("NameIndex: %v", err)
	}
	if gotLimit != nameIndexMax {
		t.Errorf("Lookup limit = %d, want %d", gotLimit, nameIndexMax)
	}
}

// The cached read serves repeats within the TTL without a catalog lookup, and
// a write to the world drops it so the next read is live again.
func TestNameIndexCachedServesThenInvalidates(t *testing.T) {
	var lookups int
	gw := fakeGateway{raw: domain.RawDocument{Body: worldMapCatalog}, limit: &lookups}
	svc := NewReadingService(gw, fakeRenderer{}, nil)
	for i := range 2 {
		lookups = 0
		got, err := svc.NameIndexCached(t.Context(), "world-a")
		if err != nil || len(got) != 5 {
			t.Fatalf("read %d: %d entries, err %v", i, len(got), err)
		}
		if read := lookups != 0; read != (i == 0) {
			t.Errorf("read %d: catalog lookup = %v, want only the first to read live", i, read)
		}
	}
	svc.invalidateTopology(domain.Change{World: "world-a", Path: "/x.md"})
	lookups = 0
	if _, err := svc.NameIndexCached(t.Context(), "world-a"); err != nil || lookups == 0 {
		t.Errorf("after a write the index should read live (lookups %d, err %v)", lookups, err)
	}
}

// The universe index is cached like a world's; a write to any world drops it,
// and a partial answer is returned with its error but never kept.
func TestNameIndexCachedUniverse(t *testing.T) {
	var lookups int
	gw := fakeGateway{raw: domain.RawDocument{Body: qualifyCatalog(worldMapCatalog, "world-a")}, limit: &lookups}
	svc := NewReadingService(gw, fakeRenderer{}, nil)
	for i := range 2 {
		lookups = 0
		got, err := svc.NameIndexCached(t.Context(), "")
		if err != nil || len(got) != 5 {
			t.Fatalf("read %d: %d entries, err %v", i, len(got), err)
		}
		if read := lookups != 0; read != (i == 0) {
			t.Errorf("read %d: catalog lookup = %v, want only the first to read live", i, read)
		}
	}
	svc.invalidateTopology(domain.Change{World: "world-b", Path: "/x.md"})
	lookups = 0
	if _, err := svc.NameIndexCached(t.Context(), ""); err != nil || lookups == 0 {
		t.Errorf("after a write to any world the universe index should read live (lookups %d, err %v)", lookups, err)
	}

}

// A partial universe index stands for partialTTL, error included, so a world
// that is down is not asked again on every keystroke; then it reads live.
func TestNameIndexCachedKeepsAPartialUniverseBriefly(t *testing.T) {
	var lookups int
	gw := fakeGateway{limit: &lookups, raw: domain.RawDocument{
		Body:     qualifyCatalog(worldMapCatalog, "world-a"),
		Metadata: map[string]string{"status": "partial", "failed": "1", "worlds": "2"},
	}}
	svc := NewReadingService(gw, fakeRenderer{}, nil)
	clock := time.Unix(1_000_000, 0)
	svc.names.now = func() time.Time { return clock }
	for _, tt := range []struct {
		name    string
		advance time.Duration
		live    bool
	}{
		{"first read", 0, true},
		{"repeat inside the window", partialTTL - time.Second, false},
		{"after the window", 2 * time.Second, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			clock = clock.Add(tt.advance)
			lookups = 0
			got, err := svc.NameIndexCached(t.Context(), "")
			if _, partial := errors.AsType[*domain.PartialLookupError](err); !partial || len(got) != 5 {
				t.Fatalf("read = %d entries, %v; want the 5 entries with the partial error", len(got), err)
			}
			if live := lookups != 0; live != tt.live {
				t.Errorf("catalog lookup = %v, want %v", live, tt.live)
			}
		})
	}
	svc.invalidateTopology(domain.Change{World: "world-a", Path: "/x.md"})
	lookups = 0
	if _, err := svc.NameIndexCached(t.Context(), ""); err == nil || lookups == 0 {
		t.Errorf("after a write the partial index should read live (lookups %d, err %v)", lookups, err)
	}
}

func TestNameIndexUniverseUsesLookupAll(t *testing.T) {
	var called string
	gw := fakeGateway{
		called: &called,
		raw: domain.RawDocument{Body: qualifyCatalog(worldMapCatalog, "world-a") + "\n" +
			qualifyCatalog(worldMapCatalog, "world-b")},
	}
	got, err := NewReadingService(gw, fakeRenderer{}, nil).NameIndex(t.Context(), "")
	if err != nil {
		t.Fatalf("NameIndex: %v", err)
	}
	if len(got) != 10 { // 5 docs × 2 worlds
		t.Fatalf("entries = %d, want 10", len(got))
	}
	seen := map[string]bool{}
	for _, e := range got {
		seen[e.World] = true
	}
	if !seen["world-a"] || !seen["world-b"] {
		t.Errorf("universe scope missing a world: %+v", seen)
	}
	if called != "LookupAll" {
		t.Errorf("called = %q, want LookupAll", called)
	}
}

func TestNameIndexUniverseSupportsDirectOneWorldResult(t *testing.T) {
	gw := fakeGateway{raw: domain.RawDocument{Source: "world-a:6309", Body: worldMapCatalog}}
	got, err := NewReadingService(gw, fakeRenderer{}, nil).NameIndex(t.Context(), "")
	if err != nil {
		t.Fatalf("NameIndex: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("entries = %d, want 5", len(got))
	}
	for _, entry := range got {
		if entry.World != "world-a:6309" {
			t.Errorf("world = %q, want world-a:6309", entry.World)
		}
	}
}

func TestNameIndexWorldScopePropagatesReadError(t *testing.T) {
	// Single-world scope propagates a read failure so the web adapter can map it
	// to an HTTP status — an outage must not render as "no matches".
	_, err := NewReadingService(fakeGateway{err: errTest}, fakeRenderer{}, nil).
		NameIndex(t.Context(), "world-a")
	if err == nil {
		t.Fatal("world-scope read error should propagate")
	}
}

func TestNameIndexUniverseAcceptsPartialResult(t *testing.T) {
	gw := fakeGateway{raw: domain.RawDocument{
		Body: qualifyCatalog(worldMapCatalog, "world-a") + `

## World failures

| World | Error |
|-------|-------|
| world-b | dial timeout |`,
		Metadata: map[string]string{"status": "partial", "failed": "1", "worlds": "2"},
	}}
	got, err := NewReadingService(gw, fakeRenderer{}, nil).NameIndex(t.Context(), "")
	var partial *domain.PartialLookupError
	if !errors.As(err, &partial) || partial.Failed != 1 || partial.Worlds != 2 ||
		len(partial.FailedWorlds) != 1 || partial.FailedWorlds[0] != "world-b" || len(got) != 5 {
		t.Fatalf("NameIndex partial = (%d entries, %v), want 5 entries and 1/2 partial", len(got), err)
	}
}

func TestNameIndexPropagatesCancellation(t *testing.T) {
	// A terminated request must not render a half-index — cancellation propagates.
	_, err := NewReadingService(fakeGateway{err: context.Canceled}, fakeRenderer{}, nil).
		NameIndex(t.Context(), "world-a")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestNameIndexUniversePropagatesLookupAllError(t *testing.T) {
	_, err := NewReadingService(fakeGateway{err: errTest}, fakeRenderer{}, nil).
		NameIndex(t.Context(), "")
	if !errors.Is(err, errTest) {
		t.Fatalf("err = %v, want %v", err, errTest)
	}
}

func TestNameIndexUniversePropagatesCancellation(t *testing.T) {
	gw := fakeGateway{err: context.Canceled}
	_, err := NewReadingService(gw, fakeRenderer{}, nil).NameIndex(t.Context(), "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func qualifyCatalog(body, world string) string {
	return strings.ReplaceAll(body, "| /", "| mark://"+world+"/")
}
