package web

// The spatial views as standalone permalinks (ADR 0005 decision 12), each the
// chunk-tail source for its canvas pane in spatial_panes.go.

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// SpatialHandler serves the graph and map surfaces.
type SpatialHandler struct {
	spatialPanes
	chrome chromeBuilder
}

// FloorPage serves the universe floor for the on-demand overlay (ADR 0006 §6):
// ?overlay=1 returns the bare universe-map SVG fragment, htmx-loaded into
// #universe-canvas when the reader pulls up the floor's "view as map" link. The
// floor has no standalone permalink (its home is pane zero, /t/u), so a direct
// hit without ?overlay=1 is sent to the canvas floor. Nodes extend the reader's
// current trail (from HX-Current-URL), so a click in the overlay continues the
// walk and the navigation dismisses the overlay.
func (h *SpatialHandler) FloorPage(c *echo.Context) error {
	if c.QueryParam("overlay") != "1" {
		home := trail{Panes: []paneAddr{{Kind: paneFloor}}, Focus: 0}
		return c.Redirect(http.StatusSeeOther, trailURL(home))
	}
	floor, err := h.maps.Floor(c.Request().Context())
	if err != nil {
		return presentError(c, err, "universe", "/")
	}
	t := currentTrail(c)
	return c.HTML(http.StatusOK, string(floorSVG(floor, t, t.Focus, h.terms)))
}

// GraphPage renders the graph neighborhood as a standalone permalink —
// /w/:world/g/<path> — the chunk-tail source and projection escape (decision
// 12). On the canvas the same neighborhood renders as a trail pane.
func (h *SpatialHandler) GraphPage(c *echo.Context) error {
	world := c.Param("world")
	p := "/" + c.Param("*")
	if u := canvasTrailURL(c, paneAddr{Kind: paneGraph, World: world, Value: p}); u != "" {
		return c.Redirect(http.StatusSeeOther, u)
	}
	n := h.graph.Neighborhood(world, p)
	// Single-pane permalink: nodes link to /w/ document permalinks.
	svg := graphSVG(n, func(r domain.Ref) string { return docRoute(r.World, r.Path) }, nil)
	vm := page{
		navChrome: h.chrome.build(c),
		Title:     "Graph: " + p,
		Host:      world,
		Path:      p,
		Content:   svg,
		World:     world,
		WorldPath: url.PathEscape(world),
	}
	return c.Render(http.StatusOK, templateFor(c), vm)
}

// WorldMapPage renders a world's map as a standalone permalink — /w/:world/u —
// the chunk-tail source and projection escape (ADR 0005 decision 12). On the
// canvas the same map renders as a trail pane.
func (h *SpatialHandler) WorldMapPage(c *echo.Context) error {
	world := c.Param("world")
	// A plain navigation to the standalone map lands on the canvas (with the map
	// pane); the overlay pull-up (?overlay=1) is always served as a fragment.
	if c.QueryParam("overlay") != "1" {
		if u := canvasTrailURL(c, paneAddr{Kind: paneFloor, World: world}); u != "" {
			return c.Redirect(http.StatusSeeOther, u)
		}
	}
	wm, err := h.maps.WorldMap(c.Request().Context(), world)
	if err != nil {
		return presentError(c, err, world, "/")
	}
	nav := h.chrome.build(c)

	// Overlay mode (ADR 0006 §5): the on-demand map pull-up htmx-loads this with
	// ?overlay=1. Its nodes extend the reader's trail (from HX-Current-URL), and
	// it returns a bare SVG fragment to swap into the overlay.
	if c.QueryParam("overlay") == "1" {
		t := currentTrail(c)
		opts := wmOpts{
			open: strings.Split(c.QueryParam("open"), ","),
			openURL: func(keys []string) string {
				return "/w/" + url.PathEscape(world) + "/u?overlay=1&open=" + url.QueryEscape(strings.Join(keys, ","))
			},
		}
		svg := worldMapRender(wm, func(p string) string {
			if len(t.Panes) > 0 {
				return trailURL(trailAfterClick(t, t.Focus, paneAddr{Kind: paneDoc, World: world, Value: p}))
			}
			return docRoute(world, p)
		}, worldNewURL(world, nav.Authenticated), opts)
		return c.HTML(http.StatusOK, string(svg))
	}

	// Single-pane permalink: nodes link to /w/ permalinks.
	svg := worldMapSVG(wm,
		func(p string) string { return docRoute(world, p) },
		worldNewURL(world, nav.Authenticated))
	vm := page{
		navChrome: nav,
		Title:     "Map: " + world,
		Host:      world,
		Path:      "/",
		Content:   svg,
		World:     world,
		WorldPath: url.PathEscape(world),
	}
	return c.Render(http.StatusOK, templateFor(c), vm)
}
