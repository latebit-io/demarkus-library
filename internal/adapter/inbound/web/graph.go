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
	graphLabel    = 22 // neighbor label length cap
	graphCenterR  = 9
	graphNodeR    = 6
	graphRatio    = 1.5 // x:y stretch — a wide neighborhood fills a wide overlay
	graphNodeVGap = 46  // vertical spacing budget per neighbor on an arc
	graphMinRy    = 120 // min vertical arc radius (small neighborhoods stay legible)
	graphMaxRy    = 440 // cap so a huge neighborhood doesn't run away
	graphLabelPad = 150 // horizontal room for the outward (left/right) node labels
	graphVPad     = 60  // top/bottom room for labels
)

// arrowMarker is the <defs> block defining the directional edge arrowhead,
// emitted once at the top of each graph / world-map SVG so a reference edge
// reads From→To. markerUnits="userSpaceOnUse" keeps the head a fixed size
// regardless of the (thin) edge stroke width.
// arrowMarker defines two heads: the resting #arrow and the green #arrow-hot the
// hover state swaps in (islands.js adds .edge-hot to a hovered node's edges).
const arrowMarker = `<defs><marker id="arrow" markerWidth="9" markerHeight="9" refX="8" refY="4.5" orient="auto" markerUnits="userSpaceOnUse"><path class="edge-arrow" d="M0,0 L9,4.5 L0,9 z"/></marker><marker id="arrow-hot" markerWidth="9" markerHeight="9" refX="8" refY="4.5" orient="auto" markerUnits="userSpaceOnUse"><path class="edge-arrow-hot" d="M0,0 L9,4.5 L0,9 z"/></marker></defs>`

// directedEdge draws a reference edge from (x1,y1) to (x2,y2) as an arrow
// pointing at the target, trimmed back by each endpoint's node radius so the
// line sits between the rims and the arrowhead lands just outside the target
// node instead of hiding under it. fromID/toID tag the edge with its endpoint
// node ids so a node-hover handler can light up every incident edge.
func directedEdge(b *strings.Builder, x1, y1, r1, x2, y2, r2 int, fromID, toID string) {
	dx, dy := float64(x2-x1), float64(y2-y1)
	d := math.Hypot(dx, dy)
	if d == 0 {
		return
	}
	ux, uy := dx/d, dy/d
	const gap = 3.0 // breathing room between arrow tip and target rim
	sx, sy := x1+int(ux*float64(r1)), y1+int(uy*float64(r1))
	ex, ey := x2-int(ux*(float64(r2)+gap)), y2-int(uy*(float64(r2)+gap))
	fmt.Fprintf(b, `<line class="graph-edge" x1="%d" y1="%d" x2="%d" y2="%d" data-from="%s" data-to="%s" marker-end="url(#arrow)"/>`,
		sx, sy, ex, ey, html.EscapeString(fromID), html.EscapeString(toID))
}

// GraphPage renders the graph neighborhood as a standalone permalink —
// /w/:world/g/<path> — the chunk-tail source and projection escape (decision
// 12). On the canvas the same neighborhood renders as a trail pane.
func (h *ReadingHandler) GraphPage(c *echo.Context) error {
	world := c.Param("world")
	p := "/" + c.Param("*")
	if u := canvasTrailURL(c, paneAddr{Kind: paneGraph, World: world, Value: p}); u != "" {
		return c.Redirect(http.StatusSeeOther, u)
	}
	n := h.reading.Neighborhood(world, p)
	// Single-pane permalink: nodes link to /w/ document permalinks.
	svg := graphSVG(n, func(r domain.Ref) string { return docRoute(r.World, r.Path) }, nil)
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
	}, trailDocRefs(t))
	return vm
}

// graphSVG lays out the neighborhood deterministically (server-side, no client
// physics): the center document in the middle, its neighbors on a ring —
// backlinks on the left arc, outbound links on the right — each joined to the
// center by an edge. urlFor turns each ref into its navigation target.
func graphSVG(n domain.Neighborhood, urlFor func(domain.Ref) string, onTrail map[domain.Ref]bool) template.HTML {
	if len(n.In) == 0 && len(n.Out) == 0 {
		return template.HTML(`<p class="graph-empty">No links observed yet — the neighborhood fills in as connected documents are read.</p>`) //nolint:gosec // static markup
	}
	// A wide elliptical neighborhood sized to its node count, viewBox fit tightly
	// so it fills the overlay instead of centering a small ring in a fixed box.
	// ry grows with the busier side (so arcs never crowd vertically); rx is
	// stretched wider; the canvas adds room for the outward node labels.
	maxSide := max(len(n.In), len(n.Out))
	ry := min(max(graphMinRy, maxSide*graphNodeVGap/2), graphMaxRy)
	rx := int(float64(ry) * graphRatio)
	width, height := 2*rx+2*graphLabelPad, 2*ry+2*graphVPad
	cx, cy := width/2, height/2

	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="graph" viewBox="0 0 %d %d" width="%d" height="%d" role="img" aria-label="document neighborhood">`,
		width, height, width, height)
	b.WriteString(arrowMarker)

	// Place backlinks across the left half (π/2 … 3π/2) and outbound links
	// across the right half (-π/2 … π/2); a lone node sits at the pole.
	placed := append(arcNodes(n.In, cx, cy, rx, ry, true), arcNodes(n.Out, cx, cy, rx, ry, false)...)

	// Edges first, so nodes draw on top. Direction follows the reference: an
	// outbound link points center→neighbor, a backlink points neighbor→center.
	for _, pn := range placed {
		if pn.inbound {
			directedEdge(&b, pn.x, pn.y, graphNodeR, cx, cy, graphCenterR, pn.ref.Path, n.Center.Path)
		} else {
			directedEdge(&b, cx, cy, graphCenterR, pn.x, pn.y, graphNodeR, n.Center.Path, pn.ref.Path)
		}
	}
	// Center node (data-node so hovering it lights up all its edges).
	fmt.Fprintf(&b, `<circle class="graph-center" data-node="%s" cx="%d" cy="%d" r="%d"/>`,
		html.EscapeString(n.Center.Path), cx, cy, graphCenterR)
	fmt.Fprintf(&b, `<text class="graph-center-label" x="%d" y="%d" text-anchor="middle">%s</text>`,
		cx, cy-graphCenterR-8, html.EscapeString(refTitle(n.Center)))
	// Neighbor nodes.
	for _, pn := range placed {
		dir := "in"
		if !pn.inbound {
			dir = "out"
		}
		cls := "graph-node graph-" + dir
		if onTrail[pn.ref] {
			cls += " graph-walked" // a neighbor already on your trail
		}
		fmt.Fprintf(&b, `<a href="%s" data-node="%s"><circle class="%s" cx="%d" cy="%d" r="%d"/>`,
			html.EscapeString(urlFor(pn.ref)), html.EscapeString(pn.ref.Path), html.EscapeString(cls), pn.x, pn.y, graphNodeR)
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

// arcNodes spreads refs over a half-ellipse: the left arc for backlinks, the
// right arc for outbound links, radii (rx, ry). A single node sits on the pole
// of its side.
func arcNodes(refs []domain.Ref, cx, cy, rx, ry int, inbound bool) []placedNode {
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
			x:       cx + int(float64(rx)*math.Cos(angle)),
			y:       cy + int(float64(ry)*math.Sin(angle)),
			inbound: inbound,
		})
	}
	return out
}

// trailDocRefs is the set of document refs on the trail — the graph overlay
// marks these neighbors as already-walked (ADR 0006 §4).
func trailDocRefs(t trail) map[domain.Ref]bool {
	refs := map[domain.Ref]bool{}
	for _, p := range t.Panes {
		if p.Kind == paneDoc && !domain.IsListingPath(p.Value) {
			refs[domain.Ref{World: p.World, Path: p.Value}] = true
		}
	}
	return refs
}

func trimGraphLabel(s string) string {
	runes := []rune(s)
	if len(runes) <= graphLabel {
		return s
	}
	return string(runes[:graphLabel-1]) + "…"
}
