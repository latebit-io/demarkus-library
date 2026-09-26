package web

import (
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/labstack/echo/v5"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// Hover preview cards (R3; ADR 0005 §margin, ADR 0003 htmx-hard). A
// previewable link is wrapped so a tiny server fragment loads on hover and
// shows as a popover — pure htmx + CSS, no new JS. One component serves both
// outbound body links and the margin's backlink entries (build once, both
// directions). The snippet (title, status, opening line) is read from the
// rendered-document cache, so a hover never costs a live world read.

const previewSnippetLen = 180 // characters of opening text shown on the card

// previewAnchorSeq mints document-unique CSS anchor names for the hover
// cards. Anchor positioning resolves a SHARED anchor-name to the last
// acceptable element in the whole document — not the nearest — so every
// link/card pair needs its own name. Process-wide and monotonic: previewize
// runs at request time (the doc cache stores pre-previewize HTML), so names
// never repeat across panes, overlays, or htmx fragments on one page.
var previewAnchorSeq atomic.Uint64

// previewAnchorName returns the next unique anchor name (a CSS dashed-ident).
func previewAnchorName() string {
	return "--pv-" + strconv.FormatUint(previewAnchorSeq.Add(1), 10)
}

// previewVM is the "preview" fragment's view model.
type previewVM struct {
	Title   string
	Status  string
	World   string
	Path    string
	Snippet string
	MarkURL string
	DocURL  string // /w/ permalink — the card's "open" link
}

// Preview serves the hover card fragment for a document.
// GET /w/:world/preview/*. Read from the cache (ADR 0005 decision 9): a
// preview must not spend the focused-live budget.
func (h *ReadingHandler) Preview(c *echo.Context) error {
	world := c.Param("world")
	p := "/" + c.Param("*")
	doc, err := h.reading.ReadCached(c.Request().Context(), world, p)
	if err != nil {
		// A card that can't load just doesn't appear — never an error page
		// behind a hover. Empty body keeps the CSS :not(:empty) popover hidden.
		return c.NoContent(http.StatusNoContent)
	}
	return c.Render(http.StatusOK, "preview", previewVM{
		Title:   doc.Title,
		Status:  doc.Status,
		World:   world,
		Path:    doc.Path,
		Snippet: previewSnippet(doc.HTML),
		MarkURL: "mark://" + world + doc.Path,
		DocURL:  docRoute(world, doc.Path),
	})
}

// previewURL is the hover-card endpoint for a document ref — the source for
// both body-link cards (derived from the link's /w/ route) and backlink-entry
// cards (derived from the observed edge).
func previewURL(r domain.Ref) string {
	return "/w/" + url.PathEscape(r.World) + "/preview" + r.Path
}

// refTitle names a backlink entry cheaply from its path (the full title rides
// on its hover card, which fetches the document). Zero reads for the margin.
func refTitle(r domain.Ref) string {
	name := r.Path[strings.LastIndex(r.Path, "/")+1:]
	if name == "" {
		return r.Path
	}
	return strings.TrimSuffix(name, ".md")
}

// backlinkLinks builds the margin's "referenced by" entries. urlFor turns each
// observed source into its navigation target — a trail URL on the canvas, a
// /w/ permalink on the single-doc view.
func backlinkLinks(refs []domain.Ref, urlFor func(domain.Ref) string) []backlinkVM {
	if len(refs) == 0 {
		return nil
	}
	out := make([]backlinkVM, 0, len(refs))
	for _, r := range refs {
		out = append(out, backlinkVM{
			Title:      refTitle(r),
			URL:        urlFor(r),
			PreviewURL: previewURL(r),
			Anchor:     template.CSS(previewAnchorName()), //nolint:gosec // counter-minted ident, no input reaches it
		})
	}
	return out
}

// previewize gives each in-app document link a hover card, with the request on
// the host span: htmx 4 will not boost an anchor that has its own hx-get, so
// the link would reload the page. Runs on /w/ hrefs; parse failure passes through.
func previewize(fragment string) string {
	out, err := rewriteFragment(fragment, previewizeNode)
	if err != nil {
		return fragment
	}
	return out
}

func previewizeNode(n *html.Node) {
	// Recurse first, capturing the next sibling before any wrapping mutates
	// the tree (the wrap reparents n, changing n.NextSibling).
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		previewizeNode(c)
		c = next
	}
	if n.Type != html.ElementNode || n.DataAtom != atom.A {
		return
	}
	addr, ok := anchorDocAddr(n)
	if !ok || domain.IsListingPath(addr.Value) {
		return // listings, tag pages, anchors, and external links get no card
	}
	wrapWithPreview(n, previewURL(domain.Ref{World: addr.World, Path: addr.Value}))
}

// previewTrigger loads a card on hover or keyboard focus, once per link.
const previewTrigger = "mouseenter delay:300ms once, focusin delay:300ms once"

// wrapWithPreview puts anchor under a request-carrying preview-host with an
// empty card. The pair shares a unique CSS anchor name so the card can pin to
// its link (position:fixed), escaping the pane's scroll clip where supported.
func wrapWithPreview(anchor *html.Node, src string) {
	parent := anchor.Parent
	if parent == nil {
		return
	}
	host := &html.Node{Type: html.ElementNode, DataAtom: atom.Span, Data: "span",
		Attr: []html.Attribute{
			{Key: "class", Val: "preview-host"},
			{Key: "hx-get", Val: src},
			{Key: "hx-trigger", Val: previewTrigger},
			{Key: "hx-target", Val: "find .preview-card"},
			{Key: "hx-swap", Val: "innerHTML"},
		}}
	parent.InsertBefore(host, anchor)
	parent.RemoveChild(anchor)
	host.AppendChild(anchor)

	name := previewAnchorName()
	setStyleDecl(anchor, "anchor-name:"+name)
	card := &html.Node{Type: html.ElementNode, DataAtom: atom.Span, Data: "span",
		Attr: []html.Attribute{
			{Key: "class", Val: "preview-card"},
			{Key: "role", Val: "tooltip"},
			{Key: "style", Val: "position-anchor:" + name},
		}}
	host.AppendChild(card)
}

// setStyleDecl adds a CSS declaration to a node's style attribute, merging
// with an existing one rather than emitting a duplicate style= attribute.
// (The sanitizer strips style from document HTML today, so the merge path is
// belt-and-braces for a future policy that lets one through.)
func setStyleDecl(n *html.Node, decl string) {
	for i, attr := range n.Attr {
		if attr.Key != "style" {
			continue
		}
		if existing := strings.TrimRight(strings.TrimSpace(attr.Val), ";"); existing != "" {
			n.Attr[i].Val = existing + ";" + decl
		} else {
			n.Attr[i].Val = decl
		}
		return
	}
	n.Attr = append(n.Attr, html.Attribute{Key: "style", Val: decl})
}

// previewSnippet extracts the opening prose of a rendered document for the
// card: the text of the first paragraph, trimmed to previewSnippetLen.
func previewSnippet(htmlStr string) string {
	nodes, err := parseFragment(htmlStr)
	if err != nil {
		return ""
	}
	for _, n := range nodes {
		if p := firstParagraphText(n); p != "" {
			return trimSnippet(p)
		}
	}
	return ""
}

func firstParagraphText(n *html.Node) string {
	if n.Type == html.ElementNode && n.DataAtom == atom.P {
		if t := strings.TrimSpace(textContent(n)); t != "" {
			return t
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if t := firstParagraphText(c); t != "" {
			return t
		}
	}
	return ""
}

func textContent(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

func trimSnippet(s string) string {
	runes := []rune(s)
	if len(runes) <= previewSnippetLen {
		return s
	}
	return strings.TrimRight(string(runes[:previewSnippetLen-1]), " ") + "…"
}
