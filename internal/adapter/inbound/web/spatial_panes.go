package web

import (
	"context"
	"fmt"
	"time"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus-library/internal/core/port"
)

// The spatial views as canvas panes (ADR 0005 decision 4). Both the canvas and
// SpatialHandler build them, so the ports are declared here once.

// spatialPanes builds the floor, world-map, graph and activity panes.
type spatialPanes struct {
	maps     port.MapService
	graph    port.GraphService
	terms    Terms         // the universe's display name (floor title, spine, context labels)
	pulse    port.Pulse    // recent activity for the floor and the activity pane; nil = no change feed
	identity worldIdentity // whose logos the universe map draws
}

// The map's brand reads run side by side on a cold cache, under a budget of
// their own: a world whose read is slow draws as a lobe, never a stalled map.
const (
	floorLogoFanout = 8
	floorLogoBudget = 3 * time.Second
)

// worldLogo is one world's logo read: "" when it declares none.
type worldLogo struct {
	world, logo string
}

// floorDraw readies the universe map for the trail's pane i: each readable
// world's logo, resolved once per render. A portal is an external host with
// no branding to read, and an unreadable world draws dimmed, as itself.
func (h spatialPanes) floorDraw(ctx context.Context, floor domain.Floor, t trail, i int) floorDraw {
	draw := floorDraw{trail: t, idx: i, terms: h.terms, logos: map[string]string{}}
	var worlds []string
	for _, fw := range floor.Worlds {
		if !fw.Portal && !fw.Err {
			worlds = append(worlds, fw.World.Name)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, floorLogoBudget)
	defer cancel()
	answers := h.readLogos(ctx, worlds)
	answered := make(map[string]bool, len(worlds))
collect:
	for range worlds {
		select {
		case a := <-answers:
			answered[a.world] = true
			if a.logo != "" {
				draw.logos[a.world] = a.logo
			}
		case <-ctx.Done():
			break collect
		}
	}
	// A read can outlive its context (a broker session lock does not watch
	// it), so a world still reading at the deadline keeps what is held.
	for _, world := range worlds {
		if answered[world] {
			continue
		}
		if logo := h.identity.held(world).LogoURL; logo != "" {
			draw.logos[world] = logo
		}
	}
	return draw
}

// readLogos reads each world's logo, floorLogoFanout at a time, until ctx
// ends. The channel holds every answer, so a read that finishes after its
// caller stopped listening never blocks.
func (h spatialPanes) readLogos(ctx context.Context, worlds []string) <-chan worldLogo {
	answers := make(chan worldLogo, len(worlds))
	slots := make(chan struct{}, floorLogoFanout)
	go func() {
		for _, world := range worlds {
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			go func() {
				defer func() { <-slots }()
				answers <- worldLogo{world: world, logo: h.identity.own(ctx, world).LogoURL}
			}()
		}
	}()
	return answers
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

	mode := paneMode(trailState, paneIndex)
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

	mode := paneMode(t, i)
	vm := paneVM{
		Mode:     mode,
		Kind:     paneFloor,
		FocusURL: trailURL(trailFocused(t, i)),
		Title:    h.terms.Universe,
		Label:    h.terms.UniverseLower(),
	}
	if mode != "spine" {
		// World door cards by default (ADR 0006 §5); the SVG topology is the
		// deliberate "view as map" secondary view, and holds the empty state.
		fv := &floorVM{Toggle: floorViewToggle(t, mapView)}
		if mapView || len(floor.Worlds) == 0 {
			fv.Map = floorSVG(floor, h.floorDraw(ctx, floor, t, i))
		} else {
			fv.Summary, fv.Cards = floorSummary(floor), worldCards(floor, t, i)
		}
		if h.pulse != nil && focused {
			if fv.Pulse, err = h.pulseSection(ctx, t, i); err != nil {
				return paneVM{}, err
			}
		}
		vm.Floor = fv
	}
	return vm, nil
}

// graphPane builds the graph pane on the trail canvas: nodes link to
// post-click trail URLs so a click continues the trail (decision 4). Like the
// floor, the graph carries no margin — its signals are on the nodes.
func (h spatialPanes) graphPane(ctx context.Context, t trail, i int, addr paneAddr) paneVM {
	mode := paneMode(t, i)
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
	n := h.graph.Neighborhood(ctx, addr.World, addr.Value)
	vm.Content = graphSVG(n, trailNodeURL(t, i), nil, trailDocRefs(t))
	return vm
}

// graphOverlay builds the focused document's neighborhood for the on-demand
// overlay (ADR 0006 §4). Node clicks jump from the focus, so it replaces the
// in-trail graph pane rather than docking beside it.
func (h spatialPanes) graphOverlay(ctx context.Context, t trail, addr paneAddr) graphOverlayVM {
	read := time.Now()
	n := h.graph.Neighborhood(ctx, addr.World, addr.Value)
	degree := n.Degree()
	return graphOverlayVM{
		// A doc nothing links to or from has no graph worth summoning: the
		// overlay stays out of the page so `g` is a no-op instead of opening
		// an empty canvas, and the affordances have something to gate on.
		Has:     degree > 0,
		Degree:  degree,
		Title:   refTitle(n.Center),
		Content: drawnAt(graphSVG(n, trailNodeURL(t, t.Focus), graphRecenterURL, trailDocRefs(t)), read),
	}
}

// trailNodeURL is where a graph or map node leads: the reader's trail
// extended from pane from, or the document's permalink when there is no trail
// (a standalone page).
func trailNodeURL(t trail, from int) func(domain.Ref) string {
	return func(r domain.Ref) string {
		if len(t.Panes) == 0 {
			return docRoute(r.World, r.Path)
		}
		return trailDocLink(r, t, from)
	}
}

// graphRecenterURL is the overlay fragment that redraws the graph around r.
func graphRecenterURL(r domain.Ref) string {
	return graphRoute(r.World, r.Path) + "?overlay=1"
}

// graphDegree is a doc's reference-edge count — the number the graph affordance
// shows and the condition it appears on. Store-only, so it costs no world read.
func (h spatialPanes) graphDegree(ctx context.Context, world, path string) int {
	return h.graph.Neighborhood(ctx, world, path).Degree()
}
