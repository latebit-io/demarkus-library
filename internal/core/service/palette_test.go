package service

import (
	"context"
	"errors"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

func TestNameIndexWorldScope(t *testing.T) {
	gw := fakeGateway{raw: domain.RawDocument{Body: worldMapCatalog}}
	got, err := NewReadingService(gw, fakeRenderer{}, nil).NameIndex(t.Context(), "world", "world-a")
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
	if e := byPath["/index.md"]; e.Title != "Home" || e.Status != "accepted" {
		t.Errorf("/index.md entry = %+v, want title Home status accepted", e)
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
	if _, err := NewReadingService(gw, fakeRenderer{}, nil).NameIndex(t.Context(), "world", "world-a"); err != nil {
		t.Fatalf("NameIndex: %v", err)
	}
	if gotLimit != nameIndexMaxPerWorld {
		t.Errorf("Lookup limit = %d, want %d", gotLimit, nameIndexMaxPerWorld)
	}
}

func TestNameIndexUniverseAggregates(t *testing.T) {
	gw := fakeGateway{
		worlds: []domain.WorldInfo{{Name: "world-a"}, {Name: "world-b"}},
		raw:    domain.RawDocument{Body: worldMapCatalog},
	}
	got, err := NewReadingService(gw, fakeRenderer{}, nil).NameIndex(t.Context(), "universe", "")
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
}

func TestNameIndexUniverseDegradesWithoutWorldList(t *testing.T) {
	// No world list (enrichment failure) ⇒ fall back to the reader's world, not
	// an empty index.
	gw := fakeGateway{worldsErr: errTest, raw: domain.RawDocument{Body: worldMapCatalog}}
	got, err := NewReadingService(gw, fakeRenderer{}, nil).NameIndex(t.Context(), "universe", "world-a")
	if err != nil {
		t.Fatalf("NameIndex should degrade, not error: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("entries = %d, want 5 (single-world fallback)", len(got))
	}
}

func TestNameIndexDegradesOnReadError(t *testing.T) {
	// The palette is auxiliary: a catalog that won't read (LOOKUP unsupported on
	// the transport, unreachable world) yields an empty index, not an error — the
	// page itself still signals real outages.
	got, err := NewReadingService(fakeGateway{err: errTest}, fakeRenderer{}, nil).
		NameIndex(t.Context(), "world", "world-a")
	if err != nil {
		t.Fatalf("read error should degrade to empty, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("entries = %d, want 0 on read failure", len(got))
	}
}

func TestNameIndexPropagatesCancellation(t *testing.T) {
	// A terminated request must not render a half-index — cancellation propagates.
	_, err := NewReadingService(fakeGateway{err: context.Canceled}, fakeRenderer{}, nil).
		NameIndex(t.Context(), "world", "world-a")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
