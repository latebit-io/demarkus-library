package web

import (
	"context"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// The trail canvas (ADR 0005 decisions 1–4): /t/* renders the whole spatial
// state from the URL. Reads follow the focused-live policy — the focused
// pane is fetched live (refreshing the cache), every other pane comes from
// the rendered-document cache — so one click costs one world read.

// canvasVM is the view model of the "canvas" template.
type canvasVM struct {
	Title         string // focused pane's title (the <title>)
	World         string // focused pane's world (nav context)
	Authenticated bool
	Panes         []paneVM
}

// paneVM is one pane on the canvas. The margin fields mirror the page VM so
// the shared doc-meta template serves both the single-doc view and the
// focused pane.
type paneVM struct {
	Mode     string // "spine" | "body" (full, body-only) | "focused"
	Kind     string
	FocusURL string // spine/header click: same path, focus moves here
	Gone     bool   // unfocused pane whose document no longer reads

	Title     string
	World     string
	WorldPath string
	Path      string
	Content   template.HTML

	HasMargin  bool // focused document pane: render the trust signals
	Status     string
	Tags       []tagLink
	Properties []domain.Property
	Modified   string
	Version    string
	Agent      string
	MarkURL    string
}

// Trail renders the canvas for the trail encoded at /t/*.
func (h *ReadingHandler) Trail(c *echo.Context) error {
	t, err := parseTrail(c.Param("*"), c.QueryParam("focus"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "malformed trail")
	}
	ctx := c.Request().Context()

	vm := canvasVM{
		Authenticated: c.Get(authedKey) != nil,
		Panes:         make([]paneVM, len(t.Panes)),
	}
	for i, addr := range t.Panes {
		focused := i == t.Focus
		if addr.Kind == paneFloor {
			pane, err := h.floorPaneView(ctx, t, i)
			if err != nil {
				if focused {
					return presentError(c, err, "universe", "/")
				}
				vm.Panes[i] = paneVM{Mode: "spine", Kind: paneFloor, Gone: true,
					FocusURL: trailURL(trailFocused(t, i)), Title: "Universe"}
				continue
			}
			vm.Panes[i] = pane
			continue
		}
		doc, err := h.readPane(ctx, addr, focused)
		switch {
		case err != nil && focused:
			// The pane being read is the page: real errors get real answers.
			return presentError(c, err, addr.World, addr.Value)
		case err != nil:
			// A stale shared trail shouldn't die because one waypoint was
			// archived: the pane renders as a tombstone spine, the rest of
			// the path survives.
			vm.Panes[i] = paneVM{
				Mode: "spine", Kind: addr.Kind, Gone: true,
				FocusURL: trailURL(trailFocused(t, i)),
				Title:    paneFallbackTitle(addr),
			}
		default:
			vm.Panes[i] = h.paneView(t, i, addr, doc)
		}
	}

	focusedPane := vm.Panes[t.Focus]
	vm.Title = focusedPane.Title
	vm.World = focusedPane.World
	return c.Render(http.StatusOK, "canvas", vm)
}

// floorPaneView builds the universe pane: floor data (focused-live like
// every pane), rendered as trail-aware SVG. The floor has no margin — its
// trust signals are ON the nodes (status strokes, importance sizing).
func (h *ReadingHandler) floorPaneView(ctx context.Context, t trail, i int) (paneVM, error) {
	focused := i == t.Focus
	var floor domain.Floor
	var err error
	if focused {
		floor, err = h.reading.Floor(ctx)
	} else {
		floor, err = h.reading.FloorCached(ctx)
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
		Title:    "Universe",
		World:    "universe",
	}
	if mode != "spine" {
		vm.Content = floorSVG(floor, t, i)
	}
	return vm, nil
}

// readPane reads one pane address: live for the focused pane, cached for
// the rest (ADR 0005 decision 9).
func (h *ReadingHandler) readPane(ctx context.Context, addr paneAddr, live bool) (domain.Document, error) {
	switch {
	case addr.Kind == paneTag && live:
		return h.reading.Tag(ctx, addr.World, addr.Value)
	case addr.Kind == paneTag:
		return h.reading.TagCached(ctx, addr.World, addr.Value)
	case strings.HasSuffix(addr.Value, "/") && live:
		return h.reading.Browse(ctx, addr.World, addr.Value)
	case strings.HasSuffix(addr.Value, "/"):
		return h.reading.BrowseCached(ctx, addr.World, addr.Value)
	case live:
		return h.reading.Read(ctx, addr.World, addr.Value)
	default:
		return h.reading.ReadCached(ctx, addr.World, addr.Value)
	}
}

// paneView builds one pane's view model: display mode by distance from
// focus (decision 3), links trail-ized so every href carries its post-click
// state, margin only where attention is.
func (h *ReadingHandler) paneView(t trail, i int, addr paneAddr, doc domain.Document) paneVM {
	focused := i == t.Focus

	mode := "spine"
	switch {
	case focused:
		mode = "focused"
	case i == t.Focus-1:
		mode = "body"
	}

	vm := paneVM{
		Mode:      mode,
		Kind:      addr.Kind,
		FocusURL:  trailURL(trailFocused(t, i)),
		Title:     doc.Title,
		World:     addr.World,
		WorldPath: url.PathEscape(addr.World),
		Path:      doc.Path,
		Status:    doc.Status,
	}
	if mode == "spine" {
		return vm // spines carry title + status only; no body is rendered
	}

	content := rewriteLinks(doc.HTML, addr.World, doc.Path)
	if addr.Kind == paneTag {
		content = linkifyCatalogPaths(content, addr.World)
	}
	vm.Content = template.HTML(trailizeLinks(content, t, i)) //nolint:gosec // sanitized in the markdown adapter; link passes only edit links

	if focused && addr.Kind == paneDoc && !strings.HasSuffix(addr.Value, "/") {
		vm.HasMargin = true
		vm.Tags = tagLinks(addr.World, doc.Tags)
		vm.Properties = doc.Properties
		vm.Modified = doc.Modified
		vm.Version = doc.Version
		vm.Agent = doc.Agent
		vm.MarkURL = "mark://" + addr.World + doc.Path
	}
	return vm
}

// paneFallbackTitle names a tombstone pane from its address.
func paneFallbackTitle(addr paneAddr) string {
	if addr.Kind == paneTag {
		return "Tagged: " + addr.Value
	}
	name := addr.Value[strings.LastIndex(addr.Value, "/")+1:]
	if name == "" {
		return addr.Value
	}
	return strings.TrimSuffix(name, ".md")
}
