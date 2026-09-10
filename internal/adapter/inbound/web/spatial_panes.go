package web

import (
	"context"
	"fmt"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus-library/internal/core/port"
)

// The spatial views as canvas panes (ADR 0005 decision 4). Both the canvas and
// SpatialHandler build them, so the ports are declared here once.

// spatialPanes builds the floor, world-map and graph panes.
type spatialPanes struct {
	maps  port.MapService
	graph port.GraphService
	terms Terms // the universe's display name (floor title, spine, context labels)
}

// worldMapPane builds the world-map pane on the trail canvas (focused-live
// like every pane): nodes link to post-click trail URLs so a click continues
// the trail (decision 4). Like the floor, the map carries no margin — its
// signals are on the nodes (status strokes, importance sizing).
func (h spatialPanes) worldMapPane(ctx context.Context, slot paneSlot) (paneVM, error) {
	trailState, paneIndex, addr := slot.trail, slot.index, slot.addr
	focused := paneIndex == trailState.Focus
	var wm domain.WorldMap
	var err error
	if focused {
		wm, err = h.maps.WorldMap(ctx, addr.World)
	} else {
		wm, err = h.maps.WorldMapCached(ctx, addr.World)
	}
	if err != nil {
		return paneVM{}, fmt.Errorf("build world map %s: %w", addr.World, err)
	}

	mode := "spine"
	switch {
	case focused:
		mode = "focused"
	case paneIndex == trailState.Focus-1:
		mode = "body"
	}
	vm := paneVM{
		Mode:     mode,
		Kind:     paneFloor,
		FocusURL: trailURL(trailFocused(trailState, paneIndex)),
		Title:    "Map: " + addr.World,
		World:    addr.World,
	}
	if mode == "spine" {
		return vm, nil
	}
	vm.Content = worldMapSVG(wm,
		func(p string) string {
			return trailURL(trailAfterClick(trailState, paneIndex, paneAddr{Kind: paneDoc, World: addr.World, Value: p}))
		},
		worldNewURL(addr.World, slot.authed))
	return vm, nil
}

// floorPane builds the universe pane: floor data (focused-live like
// every pane), rendered as trail-aware SVG. The floor has no margin — its
// trust signals are ON the nodes (status strokes, importance sizing).
func (h spatialPanes) floorPane(ctx context.Context, t trail, i int, mapView bool) (paneVM, error) {
	focused := i == t.Focus
	var floor domain.Floor
	var err error
	if focused {
		floor, err = h.maps.Floor(ctx)
	} else {
		floor, err = h.maps.FloorCached(ctx)
	}
	if err != nil {
		return paneVM{}, fmt.Errorf("build universe floor: %w", err)
	}

	mode := "spine"
	switch {
	case focused:
		mode = "focused"
	case i == t.Focus-1:
		mode = "body"
	}
	vm := paneVM{
		Mode:     mode,
		Kind:     paneFloor,
		FocusURL: trailURL(trailFocused(t, i)),
		Title:    h.terms.Universe,
		World:    h.terms.UniverseLower(),
	}
	if mode != "spine" {
		// Worlds-only door cards by default (ADR 0006 §5); the SVG topology is
		// the deliberate "view as map" secondary view.
		body := floorCards(floor, t, i, h.terms)
		if mapView {
			body = floorSVG(floor, t, i, h.terms)
		}
		vm.Content = floorViewToggle(t, mapView) + body
	}
	return vm, nil
}

// graphPane builds the graph pane on the trail canvas: nodes link to
// post-click trail URLs so a click continues the trail (decision 4). Like the
// floor, the graph carries no margin — its signals are on the nodes.
func (h spatialPanes) graphPane(t trail, i int, addr paneAddr) paneVM {
	mode := "spine"
	switch i {
	case t.Focus:
		mode = "focused"
	case t.Focus - 1:
		mode = "body"
	}
	vm := paneVM{
		Mode:     mode,
		Kind:     paneGraph,
		FocusURL: trailURL(trailFocused(t, i)),
		Title:    "Graph: " + refTitle(domain.Ref{Path: addr.Value}),
		World:    addr.World,
		Path:     addr.Value,
	}
	if mode == "spine" {
		return vm
	}
	n := h.graph.Neighborhood(addr.World, addr.Value)
	vm.Content = graphSVG(n, func(r domain.Ref) string {
		return trailURL(trailAfterClick(t, i, paneAddr{Kind: paneDoc, World: r.World, Value: r.Path}))
	}, trailDocRefs(t))
	return vm
}

// graphOverlay builds the focused document's neighborhood for the on-demand
// overlay (ADR 0006 §4). Node clicks jump from the focus, so it replaces the
// in-trail graph pane rather than docking beside it.
func (h spatialPanes) graphOverlay(t trail, addr paneAddr) graphOverlayVM {
	n := h.graph.Neighborhood(addr.World, addr.Value)
	return graphOverlayVM{
		Has:   true,
		Title: refTitle(n.Center),
		Content: graphSVG(n, func(r domain.Ref) string {
			return trailURL(trailAfterClick(t, t.Focus, paneAddr{Kind: paneDoc, World: r.World, Value: r.Path}))
		}, trailDocRefs(t)),
	}
}
