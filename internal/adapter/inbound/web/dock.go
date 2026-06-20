package web

import (
	"slices"
	"strings"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// The trail dock (ADR 0006 §2 / D2): the bottom orientation strip. It does the
// jobs a per-world column stack structurally can't — show the whole path at a
// glance, cross-world hops, walk-vs-jump connectors, and a "from here →" set of
// one-hop reference neighbors to extend into. The left spine is demoted to pure
// spatial re-expand rails; this strip carries orientation + backtrack.
//
// Everything here is server-rendered from the parsed trail; `via` is derived,
// not stored (D5): a hop is a "walk" when a reference edge joins the two panes
// in the observed graph, else a "jump". The graph is best-effort (the
// render-time observed-links map), so connectors sharpen as you navigate.

// dockMaxChips caps the "from here →" affordances — a nudge, not a directory.
const dockMaxChips = 6

type dockVM struct {
	Entries []dockEntry
	Chips   []dockChip
}

// dockEntry is one waypoint on the strip. Walked styles the connector into it
// (solid for a walked reference edge, dashed for a jump); ShowWorld marks a
// cross-world hop so the strip reads "universe → world-a → …".
type dockEntry struct {
	Label     string
	World     string
	URL       string // rewind: focus this pane (trailFocused)
	Active    bool
	First     bool
	Walked    bool
	ShowWorld bool
}

// dockChip is a one-hop reference neighbor of the active doc not yet on the
// trail — the dock's only forward-exploration affordance (deeper is the graph's).
type dockChip struct {
	Label string
	URL   string
}

// buildDock assembles the dock from the parsed trail. Call it after the pane
// loop so the focused doc's links are already observed (RecordLinks ran during
// render), giving the connectors and chips something to read.
func (h *ReadingHandler) buildDock(t trail) dockVM {
	d := dockVM{Entries: make([]dockEntry, len(t.Panes))}
	for i, p := range t.Panes {
		label, _ := paneLabel(p)
		e := dockEntry{
			Label:     label,
			World:     dockWorld(p),
			URL:       trailURL(trailFocused(t, i)),
			Active:    i == t.Focus,
			First:     i == 0,
			ShowWorld: i == 0 || dockWorld(p) != dockWorld(t.Panes[i-1]),
		}
		if i > 0 {
			e.Walked = h.refEdgeBetween(t.Panes[i-1], p)
		}
		d.Entries[i] = e
	}
	d.Chips = h.dockChips(t)
	return d
}

// dockWorld is the world shown for a pane — the floor has none, so it reads as
// "universe" (the strip's left anchor).
func dockWorld(p paneAddr) string {
	if p.Kind == paneFloor && p.World == "" {
		return "universe"
	}
	return p.World
}

// refEdgeBetween reports whether a reference edge joins two doc panes in the
// observed graph (either direction) — the test for a "walk" vs a "jump".
func (h *ReadingHandler) refEdgeBetween(a, b paneAddr) bool {
	if !isDocPane(a) || !isDocPane(b) {
		return false
	}
	bref := domain.Ref{World: b.World, Path: b.Value}
	n := h.reading.Neighborhood(a.World, a.Value)
	return slices.Contains(n.Out, bref) || slices.Contains(n.In, bref)
}

// dockChips lists up to dockMaxChips one-hop reference neighbors of the active
// doc that are not already on the trail.
func (h *ReadingHandler) dockChips(t trail) []dockChip {
	if t.Focus < 0 || t.Focus >= len(t.Panes) {
		return nil
	}
	f := t.Panes[t.Focus]
	if !isDocPane(f) {
		return nil
	}
	onTrail := map[domain.Ref]bool{}
	for _, p := range t.Panes {
		if isDocPane(p) {
			onTrail[domain.Ref{World: p.World, Path: p.Value}] = true
		}
	}
	n := h.reading.Neighborhood(f.World, f.Value)
	var chips []dockChip
	seen := map[domain.Ref]bool{}
	for _, r := range append(append([]domain.Ref{}, n.Out...), n.In...) {
		if onTrail[r] || seen[r] {
			continue
		}
		seen[r] = true
		chips = append(chips, dockChip{
			Label: docName(r.Path),
			URL:   trailURL(trailAfterClick(t, t.Focus, paneAddr{Kind: paneDoc, World: r.World, Value: r.Path})),
		})
		if len(chips) >= dockMaxChips {
			break
		}
	}
	return chips
}

// isDocPane reports whether a pane addresses a real document (not a listing,
// tag, floor, or graph pane) — the only kind that participates in the reference
// graph the connectors and chips read.
func isDocPane(p paneAddr) bool {
	return p.Kind == paneDoc && !domain.IsListingPath(p.Value)
}

// docName is a path's display name: the last segment without its .md.
func docName(path string) string {
	name := strings.TrimSuffix(path[strings.LastIndex(path, "/")+1:], ".md")
	if name == "" {
		return path
	}
	return name
}
