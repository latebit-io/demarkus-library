package web

import (
	"fmt"
	"html"
	"html/template"
	"math"
	"net/http"
	"net/url"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// The graph neighborhood pane (R3; ADR 0005 decisions 4/5). One document with
// its observed outbound links and backlinks, rendered as SSR SVG — same
// approach as the floor, so ADR 0003's canvas island stays unspent. Every node
// is a plain <a> whose href continues the trail (walking the graph IS building
// a trail). Edges come from the render-time observed-links map, so the pane
// works in both transports and is simply sparse until the documents are read.
const (
	graphW       = 760
	graphH       = 440
	graphRingR   = 165 // neighbor ring radius
	graphLabel   = 22  // neighbor label length cap
	graphCenterR = 9
	graphNodeR   = 6
)

// GraphPage renders the graph neighborhood as a standalone permalink —
// /w/:world/g/<path> — the chunk-tail source and projection escape (decision
// 12). On the canvas the same neighborhood renders as a trail pane.
func (h *ReadingHandler) GraphPage(c *echo.Context) error {
	world := c.Param("world")
	p := "/" + c.Param("*")
	n := h.reading.Neighborhood(world, p)
	// Single-pane permalink: nodes link to /w/ document permalinks.
	svg := graphSVG(n, func(r domain.Ref) string { return docRoute(r.World, r.Path) })
	vm := page{
		Title:         "Graph: " + p,
		Host:          world,
		Path:          p,
		Content:       svg,
		World:         world,
		WorldPath:     url.PathEscape(world),
		Authenticated: c.Get(authedKey) != nil,
		User:          userEmail(c),
	}
	return c.Render(http.StatusOK, h.templateFor(c), vm)
}

// graphPaneView builds the graph pane on the trail canvas: nodes link to
// post-click trail URLs so a click continues the trail (decision 4). Like the
// floor, the graph carries no margin — its signals are on the nodes.
func (h *ReadingHandler) graphPaneView(t trail, i int, addr paneAddr) paneVM {
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
	n := h.reading.Neighborhood(addr.World, addr.Value)
	vm.Content = graphSVG(n, func(r domain.Ref) string {
		return trailURL(trailAfterClick(t, i, paneAddr{Kind: paneDoc, World: r.World, Value: r.Path}))
	})
	return vm
}

// graphSVG lays out the neighborhood deterministically (server-side, no client
// physics): the center document in the middle, its neighbors on a ring —
// backlinks on the left arc, outbound links on the right — each joined to the
// center by an edge. urlFor turns each ref into its navigation target.
func graphSVG(n domain.Neighborhood, urlFor func(domain.Ref) string) template.HTML {
	if len(n.In) == 0 && len(n.Out) == 0 {
		return template.HTML(`<p class="graph-empty">No links observed yet — the neighborhood fills in as connected documents are read.</p>`) //nolint:gosec // static markup
	}
	cx, cy := graphW/2, graphH/2

	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="graph" viewBox="0 0 %d %d" width="%d" height="%d" role="img" aria-label="document neighborhood">`,
		graphW, graphH, graphW, graphH)

	// Place backlinks across the left half (π/2 … 3π/2) and outbound links
	// across the right half (-π/2 … π/2); a lone node sits at the pole.
	placed := append(arcNodes(n.In, cx, cy, true), arcNodes(n.Out, cx, cy, false)...)

	// Edges first, so nodes draw on top.
	for _, pn := range placed {
		fmt.Fprintf(&b, `<line class="graph-edge" x1="%d" y1="%d" x2="%d" y2="%d"/>`, cx, cy, pn.x, pn.y)
	}
	// Center node.
	fmt.Fprintf(&b, `<circle class="graph-center" cx="%d" cy="%d" r="%d"/>`, cx, cy, graphCenterR)
	fmt.Fprintf(&b, `<text class="graph-center-label" x="%d" y="%d" text-anchor="middle">%s</text>`,
		cx, cy-graphCenterR-8, html.EscapeString(refTitle(n.Center)))
	// Neighbor nodes.
	for _, pn := range placed {
		dir := "in"
		if !pn.inbound {
			dir = "out"
		}
		fmt.Fprintf(&b, `<a href="%s"><circle class="graph-node graph-%s" cx="%d" cy="%d" r="%d"/>`,
			html.EscapeString(urlFor(pn.ref)), dir, pn.x, pn.y, graphNodeR)
		anchor := "middle"
		if pn.x < cx {
			anchor = "end"
		} else if pn.x > cx {
			anchor = "start"
		}
		fmt.Fprintf(&b, `<text class="graph-node-label" x="%d" y="%d" text-anchor="%s">%s</text>`,
			pn.x, pn.y-graphNodeR-6, anchor, html.EscapeString(trimGraphLabel(refTitle(pn.ref))))
		fmt.Fprintf(&b, `<title>%s — %s</title></a>`, html.EscapeString(pn.ref.Path), html.EscapeString(pn.ref.World))
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String()) //nolint:gosec // built from escaped parts; all text/attrs pass html.EscapeString
}

// placedNode is a neighbor with its computed position.
type placedNode struct {
	ref     domain.Ref
	x, y    int
	inbound bool
}

// arcNodes spreads refs over a half-circle: the left arc for backlinks, the
// right arc for outbound links. A single node sits on the pole of its side.
func arcNodes(refs []domain.Ref, cx, cy int, inbound bool) []placedNode {
	out := make([]placedNode, 0, len(refs))
	for j, r := range refs {
		var frac float64
		if len(refs) > 1 {
			frac = float64(j) / float64(len(refs)-1)
		} else {
			frac = 0.5
		}
		// Sweep top→bottom across the half; mirror to the correct side.
		angle := -math.Pi/2 + frac*math.Pi
		if inbound {
			angle = math.Pi - angle
		}
		out = append(out, placedNode{
			ref:     r,
			x:       cx + int(graphRingR*math.Cos(angle)),
			y:       cy + int(graphRingR*math.Sin(angle)),
			inbound: inbound,
		})
	}
	return out
}

func trimGraphLabel(s string) string {
	runes := []rune(s)
	if len(runes) <= graphLabel {
		return s
	}
	return string(runes[:graphLabel-1]) + "…"
}
