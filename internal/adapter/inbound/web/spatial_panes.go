package web

import (
	"context"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus-library/internal/core/port"
)

// The spatial views as canvas panes (ADR 0005 decision 4): the universe floor,
// a world's map, and a document's graph neighborhood. The trail canvas composes
// every pane kind, so it holds one of these rather than growing the map and
// graph ports itself; SpatialHandler holds one too, which is why the ports and
// the display vocabulary are declared here once.

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
func (h spatialPanes) worldMapPane(ctx context.Context, t trail, i int, addr paneAddr, authed bool) (paneVM, error) {
	focused := i == t.Focus
	var wm domain.WorldMap
	var err error
	if focused {
		wm, err = h.maps.WorldMap(ctx, addr.World)
	} else {
		wm, err = h.maps.WorldMapCached(ctx, addr.World)
	}
	if err != nil {
		return paneVM{}, err
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
		Title:    "Map: " + addr.World,
		World:    addr.World,
	}
	if mode == "spine" {
		return vm, nil
	}
	vm.Content = worldMapSVG(wm,
		func(p string) string {
			return trailURL(trailAfterClick(t, i, paneAddr{Kind: paneDoc, World: addr.World, Value: p}))
		},
		worldNewURL(addr.World, authed))
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
		return paneVM{}, err
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

// graphOverlay builds the focused document's reference neighborhood for the
// on-demand overlay (ADR 0006 §4), summoned by `g`. Node clicks are trail jumps
// from the focus, so the overlay replaces the in-trail graph pane rather than
// docking beside it.
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
