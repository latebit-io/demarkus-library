package web

import (
	"strings"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

func TestBuildDock(t *testing.T) {
	// universe → world-a/index.md (jump) → world-a/mission.md (walk; index links
	// to mission in the observed graph). Focus is the last pane.
	svc := &fakeReading{neighbor: map[string]domain.Neighborhood{
		"/index.md":   {Out: []domain.Ref{{World: "world-a", Path: "/mission.md"}}},
		"/mission.md": {Out: []domain.Ref{{World: "world-a", Path: "/vision.md"}}},
	}}
	h := NewReadingHandler(svc, "world-a", "/index.md")
	tr := trail{Panes: []paneAddr{
		{Kind: paneFloor},
		{Kind: paneDoc, World: "world-a", Value: "/index.md"},
		{Kind: paneDoc, World: "world-a", Value: "/mission.md"},
	}, Focus: 2, Reader: -1}

	d := h.buildDock(tr)

	if len(d.Entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(d.Entries))
	}
	if d.Entries[1].Walked {
		t.Error("floor→index is a jump, must not be walked")
	}
	if !d.Entries[2].Walked {
		t.Error("index→mission has a reference edge, must be walked")
	}
	if !d.Entries[2].Active {
		t.Error("focused (last) entry must be active")
	}
	// Cross-world labels: universe on the floor, world-a once (first world-a pane).
	if d.Entries[0].World != "universe" || !d.Entries[0].ShowWorld {
		t.Errorf("floor entry = %+v, want world universe + ShowWorld", d.Entries[0])
	}
	if !d.Entries[1].ShowWorld || d.Entries[2].ShowWorld {
		t.Error("world tag should show on the first world-a pane only")
	}
	// from-here chips: mission's neighbor vision.md (not already on the trail).
	if len(d.Chips) != 1 || d.Chips[0].Label != "vision" {
		t.Fatalf("chips = %+v, want one 'vision'", d.Chips)
	}
	if !strings.Contains(d.Chips[0].URL, "/world-a/d/vision.md") {
		t.Errorf("chip URL should extend the trail to vision: %s", d.Chips[0].URL)
	}
}

func TestBuildDockChipsSkipPanesAlreadyOnTrail(t *testing.T) {
	// mission links to index, but index is already on the trail ⇒ no chip.
	svc := &fakeReading{neighbor: map[string]domain.Neighborhood{
		"/mission.md": {Out: []domain.Ref{{World: "world-a", Path: "/index.md"}}},
	}}
	h := NewReadingHandler(svc, "world-a", "/index.md")
	tr := trail{Panes: []paneAddr{
		{Kind: paneDoc, World: "world-a", Value: "/index.md"},
		{Kind: paneDoc, World: "world-a", Value: "/mission.md"},
	}, Focus: 1, Reader: -1}

	if d := h.buildDock(tr); len(d.Chips) != 0 {
		t.Errorf("chips = %+v, want none (only neighbor is already on the trail)", d.Chips)
	}
}
