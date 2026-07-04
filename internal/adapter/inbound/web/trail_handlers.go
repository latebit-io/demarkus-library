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

// The overlay lenses a pane can be built for (paneView's overlay param):
// none (a canvas pane), the reader (R4), or the metadata record.
const (
	overlayNone   = ""
	overlayReader = "reader"
	overlayMeta   = "meta"
)

// canvasVM is the view model of the "canvas" template.
type canvasVM struct {
	Title         string // focused pane's title (the <title>)
	World         string // focused pane's world (nav context)
	Authenticated bool
	User          string // signed-in identity's email for the nav (empty ⇒ not shown)
	LibrarianURL  string // nav door: append the librarian pane to THIS trail (empty ⇒ not configured)
	Panes         []paneVM
	Reader        *paneVM        // the reader overlay (R4); nil when closed
	MetaPane      *paneVM        // the metadata overlay (the record lens); nil when closed
	CloseURL      string         // ✕ / backdrop / Esc target: the bare trail (no overlay)
	Dock          dockVM         // the bottom orientation strip (ADR 0006 §2)
	Graph         graphOverlayVM // the on-demand graph overlay (ADR 0006 §4)

	// The world-map overlay shell (ADR 0006 §5, on-demand discovery): empty
	// until summoned, then htmx-loaded lazily so an unopened map costs no read.
	MapHas       bool
	MapWorld     string
	MapWorldPath string

	// The universe overlay shell (ADR 0006 §6): present whenever the universe
	// floor is on the canvas, where its "view as map" trigger lives. Lazy — the
	// floorSVG htmx-loads only when the reader pulls it up.
	FloorHas bool

	// PaneScroll marks the independent-pane-scroll experiment: the canvas
	// template adds a body class and the stylesheet does the rest.
	PaneScroll bool
}

// graphOverlayVM is the focused doc's graph overlay (ADR 0006 §4): summoned by
// `g`, it replaces the in-trail graph pane. Has is false when the focused pane
// is not a document (then `g` has nothing to show).
type graphOverlayVM struct {
	Has     bool
	Title   string
	Content template.HTML
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
	Type       string // OKF `type`: the document kind (grouped with Tags/Modified in doc-meta)
	Tags       []tagLink
	Properties []domain.Property
	Modified   string
	Version    string
	Agent      string
	Meta       []domain.Property // every other out-of-band metadata key, sorted
	MarkURL    string
	Librarian  *librarianPaneVM // the librarian pane's transcript + ask form (kind "a" only)
	ReaderURL  string           // header/margin affordance: open this pane in the reader overlay (R4)
	GraphURL   string           // margin affordance: open this doc's graph pane
	MapURL     string           // margin affordance: open this world's map (zoom level 2)
	EditURL    string           // margin affordance: edit this doc (Phase 3); only when authed
	NewURL     string           // margin affordance: create a doc in this folder (Phase 3); only when authed
	AppendURL  string           // margin affordance: append to this doc (Phase 3); only when authed
	Backlinks  []backlinkVM     // "referenced by" — the observed-links map

	// The metadata lens (pane-scroll room): MetaURL is the pane-head
	// affordance that opens this doc's catalog record as an overlay
	// (?meta=<i>); MetaOpen renders the record's fold pre-expanded (set on
	// the overlay's own pane, where the record is the point). HeadStatus
	// puts the status chip in the pane head — the marginless room's at-rest
	// trust signal (the badge otherwise waits behind the lens).
	MetaURL    string
	MetaOpen   bool
	HeadStatus string
}

// Trail renders the canvas for the trail encoded at /t/*.
func (h *ReadingHandler) Trail(c *echo.Context) error {
	t, err := parseTrail(c.Param("*"), c.QueryParam("focus"), c.QueryParam("reader"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "malformed trail")
	}
	t = t.withMeta(c.QueryParam("meta"))
	ctx := c.Request().Context()
	authed := c.Get(authedKey) != nil

	vm := canvasVM{
		Authenticated: authed,
		User:          userEmail(c),
		Panes:         make([]paneVM, len(t.Panes)),
		PaneScroll:    h.paneScroll,
	}
	if h.lib != nil {
		// The nav's librarian door appends the pane to the CURRENT trail —
		// the conversation arrives with the reader's context, and the click
		// algebra focuses an already-present librarian instead of duplicating.
		vm.LibrarianURL = trailURL(trailAfterClick(t, t.Focus, paneAddr{Kind: paneLibrarian}))
	}
	// The reader overlay (R4) is a lens over whichever pane ?reader= names —
	// independent of focus. Every pane is read in this loop anyway (focused
	// live, the rest cached), so capture the reader pane's already-fetched doc
	// here to render the overlay without a second world read.
	var readerDoc domain.Document
	var readerAddr paneAddr
	var haveReaderDoc bool
	var metaDoc domain.Document
	var metaAddr paneAddr
	var haveMetaDoc bool
	for i, addr := range t.Panes {
		focused := i == t.Focus
		if addr.Kind == paneFloor {
			// Bare "u" is the universe floor; "<world>/u/" is that world's map
			// (the same component one zoom in).
			var pane paneVM
			var err error
			scope := "universe"
			if addr.World == "" {
				// The universe floor is on the canvas → its "view as map"
				// trigger exists, so render the overlay shell (ADR 0006 §6).
				vm.FloorHas = true
				pane, err = h.floorPaneView(ctx, t, i, c.QueryParam("view") == "map")
			} else {
				scope = addr.World
				pane, err = h.worldMapPaneView(ctx, t, i, addr, authed)
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
		if addr.Kind == paneLibrarian {
			// The librarian pane renders from server-side conversation state —
			// no world read, never a tombstone (a librarian problem is a
			// notice inside the pane, not a Gone spine).
			vm.Panes[i] = h.librarianPaneView(c, t, i)
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
			if i == t.Reader {
				readerDoc, readerAddr, haveReaderDoc = doc, addr, true
			}
			if i == t.Meta {
				metaDoc, metaAddr, haveMetaDoc = doc, addr, true
			}
			vm.Panes[i] = h.paneView(ctx, t, i, addr, doc, authed, overlayNone)
		}
	}

	focusedPane := vm.Panes[t.Focus]
	vm.Title = focusedPane.Title
	vm.World = focusedPane.World

	// The world-map overlay is available whenever the focus sits in a world (a
	// doc, listing, tag, or world-map pane) — the bare universe floor has none.
	if fw := t.Panes[t.Focus].World; fw != "" {
		vm.MapHas = true
		vm.MapWorld = fw
		vm.MapWorldPath = url.PathEscape(fw)
	}

	// The dock is built after the pane loop so the focused doc's links are
	// already observed (RecordLinks ran during render), giving the walk/jump
	// connectors and "from here →" chips a graph to read.
	vm.Dock = h.buildDock(t)

	// The graph overlay (ADR 0006 §4): the focused doc's reference neighborhood,
	// summoned by `g`. Built here (links observed), embedded hidden — it replaces
	// the in-trail graph pane. Node clicks are trail jumps from the focus.
	if fa := t.Panes[t.Focus]; fa.Kind == paneDoc && !domain.IsListingPath(fa.Value) {
		n := h.reading.Neighborhood(fa.World, fa.Value)
		vm.Graph = graphOverlayVM{
			Has:   true,
			Title: refTitle(n.Center),
			Content: graphSVG(n, func(r domain.Ref) string {
				return trailURL(trailAfterClick(t, t.Focus, paneAddr{Kind: paneDoc, World: r.World, Value: r.Path}))
			}, trailDocRefs(t)),
		}
	}

	// The reader overlay reuses the addressed pane's already-fetched document —
	// no extra world read (the overlay is pure presentation), and focus is
	// untouched so the canvas behind is unchanged. Body links persist the
	// overlay (reader=true); ✕/backdrop/Esc close to trailURL(t), which keeps
	// the original focus.
	if t.Reader >= 0 && haveReaderDoc {
		rp := h.paneView(ctx, t, t.Reader, readerAddr, readerDoc, authed, overlayReader)
		vm.Reader = &rp
		vm.CloseURL = trailURL(t)
	}

	// The metadata overlay (the record lens): the addressed pane's catalog
	// card, reusing its already-fetched document like the reader does — no
	// extra world read, no re-recorded edges.
	if t.Meta >= 0 && haveMetaDoc {
		mp := h.paneView(ctx, t, t.Meta, metaAddr, metaDoc, authed, overlayMeta)
		if mp.HasMargin {
			mp.MetaOpen = true // the fold arrives expanded — this overlay IS the ask
			vm.MetaPane = &mp
			vm.CloseURL = trailURL(t)
		}
	}
	return c.Render(http.StatusOK, "canvas", vm)
}

// floorPaneView builds the universe pane: floor data (focused-live like
// every pane), rendered as trail-aware SVG. The floor has no margin — its
// trust signals are ON the nodes (status strokes, importance sizing).
func (h *ReadingHandler) floorPaneView(ctx context.Context, t trail, i int, mapView bool) (paneVM, error) {
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
		// Worlds-only door cards by default (ADR 0006 §5); the SVG topology is
		// the deliberate "view as map" secondary view.
		body := floorCards(floor, t, i)
		if mapView {
			body = floorSVG(floor, t, i)
		}
		vm.Content = floorViewToggle(t, mapView) + body
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
	case live:
		return h.reading.Open(ctx, addr.World, addr.Value)
	default:
		return h.reading.OpenCached(ctx, addr.World, addr.Value)
	}
}

// paneView builds one pane's view model: display mode by distance from
// focus (decision 3), links trail-ized so every href carries its post-click
// state, margin only where attention is.
// overlay marks a pane built for a lens instead of the canvas: the reader
// overlay (R4) persists itself in body/backlink hrefs, the metadata overlay
// keeps plain trail links (a record click is a navigation, not more lens).
// Either way the pane carries the full margin, records no edges (the canvas
// build already did), and offers no open-overlay affordances (it IS one).
func (h *ReadingHandler) paneView(ctx context.Context, t trail, i int, addr paneAddr, doc domain.Document, authed bool, overlay string) paneVM {
	focused := i == t.Focus
	reader := overlay == overlayReader

	mode := "spine"
	switch {
	case overlay != "":
		mode = overlay
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
	if overlay == "" && (addr.Kind == paneDoc || addr.Kind == paneTag) {
		vm.ReaderURL = trailReaderURL(t, i)
	}
	// In the pane-scroll room the margin is summoned, not docked: the head
	// offers "meta" beside "reader", the record opens as an overlay, and the
	// status chip keeps the room's one at-rest trust signal visible.
	if h.paneScroll && overlay == "" && addr.Kind == paneDoc && !domain.IsListingPath(addr.Value) {
		vm.MetaURL = trailMetaURL(t, i)
		vm.HeadStatus = doc.Status
	}
	if mode == "spine" {
		return vm // spines carry title + status only; no body is rendered
	}

	content, edges := rewriteLinks(doc.HTML, addr.World, doc.Path)
	if overlay == "" && addr.Kind == paneDoc && !domain.IsListingPath(addr.Value) {
		// Feed the observed-links map (R3) from real document panes only —
		// listings and tag pages are not edge sources. This runs for the
		// focused pane and its body-only parent, so a doc's edges are recorded
		// just before its graph/backlinks pane (to the right) reads them. The
		// overlays reuse already-rendered panes, so skip them (recorded once).
		h.reading.RecordLinks(addr.World, doc.Path, edges)
	}
	if addr.Kind == paneTag {
		content = linkifyCatalogPaths(content, addr.World)
	}
	if focused && addr.Kind == paneDoc && domain.IsListingPath(addr.Value) {
		// Rich index (ADR 0006 §5): enrich the focused listing pane's bare ls
		// before its links are trailized. Focused-only keeps the per-click read
		// budget (an unfocused listing stays a plain ls — it's context).
		content = h.richIndex(ctx, addr.World, content)
	}
	// previewize runs on /w/ hrefs (it derives each card's source from them),
	// then trailizeLinks rewrites those hrefs to post-click trail URLs — in the
	// overlay (reader=true) those become reader-persisting URLs.
	vm.Content = template.HTML(trailizeLinks(previewize(content), t, i, reader)) //nolint:gosec // sanitized in the markdown adapter; these passes only rewrite/wrap links, adding no unescaped content

	// An overlay shows the addressed pane's full margin even when it is not
	// the focused pane — a lens is not a dead-end.
	if (focused || overlay != "") && addr.Kind == paneDoc && !domain.IsListingPath(addr.Value) {
		vm.HasMargin = true
		vm.Type = doc.Type
		vm.Tags = tagLinks(addr.World, doc.Tags)
		vm.Properties = doc.Properties
		vm.Modified = doc.Modified
		vm.Version = doc.Version
		vm.Agent = doc.Agent
		vm.Meta = doc.Meta
		vm.MarkURL = "mark://" + addr.World + doc.Path
		// Graph/map open non-prose panes, so they exit the overlay (plain
		// trail URLs) even in reader mode; backlinks point at docs, so they
		// persist the overlay like any other prose link.
		// The "graph" affordance opens the graph overlay (ADR 0006 §4); the href
		// is the /g/ permalink so it degrades to the standalone graph page when
		// JS is off (islands.js intercepts it on the canvas, where the overlay
		// exists). The graph is no longer docked as a trail pane.
		vm.GraphURL = "/w/" + url.PathEscape(addr.World) + "/g" + addr.Value
		// "map" opens the world-map overlay (ADR 0006 §5); the href is the /u
		// permalink so it degrades to the standalone map page without JS. The
		// map is no longer docked as a trail pane.
		vm.MapURL = "/w/" + url.PathEscape(addr.World) + "/u"
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
