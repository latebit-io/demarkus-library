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
	Reader        *paneVM // the reader overlay (R4); nil when closed
	CloseURL      string  // ✕ / backdrop / Esc target: the bare trail (no overlay)
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
	ReaderURL  string       // header/margin affordance: open this pane in the reader overlay (R4)
	GraphURL   string       // margin affordance: open this doc's graph pane
	MapURL     string       // margin affordance: open this world's map (zoom level 2)
	EditURL    string       // margin affordance: edit this doc (Phase 3); only when authed
	NewURL     string       // margin affordance: create a doc in this folder (Phase 3); only when authed
	AppendURL  string       // margin affordance: append to this doc (Phase 3); only when authed
	Backlinks  []backlinkVM // "referenced by" — the observed-links map
}

// Trail renders the canvas for the trail encoded at /t/*.
func (h *ReadingHandler) Trail(c *echo.Context) error {
	t, err := parseTrail(c.Param("*"), c.QueryParam("focus"), c.QueryParam("reader"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "malformed trail")
	}
	ctx := c.Request().Context()
	authed := c.Get(authedKey) != nil

	vm := canvasVM{
		Authenticated: authed,
		Panes:         make([]paneVM, len(t.Panes)),
	}
	// The reader overlay (R4) reuses the focused pane's live document, so a
	// valid ?reader= forces Focus onto that pane (parseTrail) — capture the
	// fetched doc here to render the overlay without a second world read.
	var focusedDoc domain.Document
	var focusedAddr paneAddr
	var haveFocusedDoc bool
	for i, addr := range t.Panes {
		focused := i == t.Focus
		if addr.Kind == paneFloor {
			// Bare "u" is the universe floor; "<world>/u/" is that world's map
			// (the same component one zoom in).
			var pane paneVM
			var err error
			scope := "universe"
			if addr.World == "" {
				pane, err = h.floorPaneView(ctx, t, i)
			} else {
				scope = addr.World
				pane, err = h.worldMapPaneView(ctx, t, i, addr)
			}
			if err != nil {
				if focused {
					return presentError(c, err, scope, "/")
				}
				vm.Panes[i] = paneVM{Mode: "spine", Kind: paneFloor, Gone: true,
					FocusURL: trailURL(trailFocused(t, i)), Title: floorSpineTitle(addr)}
				continue
			}
			vm.Panes[i] = pane
			continue
		}
		if addr.Kind == paneGraph {
			// The graph pane is store-only (no world read), so it never errors
			// and needs no live/cached split — it renders the same in both.
			vm.Panes[i] = h.graphPaneView(t, i, addr)
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
			if focused {
				focusedDoc, focusedAddr, haveFocusedDoc = doc, addr, true
			}
			vm.Panes[i] = h.paneView(t, i, addr, doc, authed, false)
		}
	}

	focusedPane := vm.Panes[t.Focus]
	vm.Title = focusedPane.Title
	vm.World = focusedPane.World

	// The reader overlay reuses the focused pane's already-fetched document —
	// no extra world read (the overlay is pure presentation). Its body links
	// persist the overlay (reader=true); ✕/backdrop/Esc close to the bare
	// trail, focus left on the pane just read.
	if t.Reader >= 0 && haveFocusedDoc {
		rp := h.paneView(t, t.Reader, focusedAddr, focusedDoc, authed, true)
		vm.Reader = &rp
		vm.CloseURL = trailURL(t)
	}
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
// When reader is true the pane is built for the reader overlay (R4): the same
// focused doc + margin, but body and backlink hrefs persist the overlay
// (reader-mode links), edges are not re-recorded (the canvas build already
// did), and the pane carries no "open reader" affordance (it is already open).
func (h *ReadingHandler) paneView(t trail, i int, addr paneAddr, doc domain.Document, authed, reader bool) paneVM {
	focused := i == t.Focus

	mode := "spine"
	switch {
	case reader:
		mode = "reader"
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
	// A prose pane offers the reader overlay; the overlay itself does not (it
	// is already open). Floor/graph panes never reach paneView, but guard the
	// kind anyway so a future caller can't mint a reader link to a non-prose
	// pane that parseTrail would reject.
	if !reader && (addr.Kind == paneDoc || addr.Kind == paneTag) {
		vm.ReaderURL = trailReaderURL(t, i)
	}
	if mode == "spine" {
		return vm // spines carry title + status only; no body is rendered
	}

	content, edges := rewriteLinks(doc.HTML, addr.World, doc.Path)
	if !reader && addr.Kind == paneDoc && !strings.HasSuffix(addr.Value, "/") {
		// Feed the observed-links map (R3) from real document panes only —
		// listings and tag pages are not edge sources. This runs for the
		// focused pane and its body-only parent, so a doc's edges are recorded
		// just before its graph/backlinks pane (to the right) reads them. The
		// overlay reuses the focused pane, so skip it there (already recorded).
		h.reading.RecordLinks(addr.World, doc.Path, edges)
	}
	if addr.Kind == paneTag {
		content = linkifyCatalogPaths(content, addr.World)
	}
	// previewize runs on /w/ hrefs (it derives each card's source from them),
	// then trailizeLinks rewrites those hrefs to post-click trail URLs — in the
	// overlay (reader=true) those become reader-persisting URLs.
	vm.Content = template.HTML(trailizeLinks(previewize(content), t, i, reader)) //nolint:gosec // sanitized in the markdown adapter; these passes only rewrite/wrap links, adding no unescaped content

	if focused && addr.Kind == paneDoc && !strings.HasSuffix(addr.Value, "/") {
		vm.HasMargin = true
		vm.Tags = tagLinks(addr.World, doc.Tags)
		vm.Properties = doc.Properties
		vm.Modified = doc.Modified
		vm.Version = doc.Version
		vm.Agent = doc.Agent
		vm.MarkURL = "mark://" + addr.World + doc.Path
		// Graph/map open non-prose panes, so they exit the overlay (plain
		// trail URLs) even in reader mode; backlinks point at docs, so they
		// persist the overlay like any other prose link.
		vm.GraphURL = trailURL(trailAfterClick(t, i, paneAddr{Kind: paneGraph, World: addr.World, Value: addr.Value}))
		vm.MapURL = trailURL(trailAfterClick(t, i, paneAddr{Kind: paneFloor, World: addr.World}))
		// Edit leaves the canvas into the dedicated editor page (a focused-pane
		// mode, not a trail chunk); only behind the turnstile.
		if authed {
			vm.EditURL = "/w/" + url.PathEscape(addr.World) + "/edit" + addr.Value
			vm.NewURL = "/w/" + url.PathEscape(addr.World) + "/new?dir=" + url.QueryEscape(dirOf(addr.Value))
			vm.AppendURL = "/w/" + url.PathEscape(addr.World) + "/append" + addr.Value
		}
		vm.Backlinks = backlinkLinks(h.reading.Backlinks(addr.World, addr.Value), func(r domain.Ref) string {
			next := trailAfterClick(t, i, paneAddr{Kind: paneDoc, World: r.World, Value: r.Path})
			if reader {
				return trailReaderURL(next, next.Focus)
			}
			return trailURL(next)
		})
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
