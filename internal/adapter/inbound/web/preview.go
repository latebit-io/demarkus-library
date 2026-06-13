package web

import (
	"bytes"
	"net/http"
	"net/url"
	"strings"

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
		})
	}
	return out
}

// previewize wraps each in-app document anchor so it shows a hover card: the
// anchor gains hx-get/hx-trigger/hx-target and is enclosed in
// <span class="preview-host">…<span class="preview-card"></span></span> for
// the CSS popover. mouseenter-as-trigger overrides the anchor's default click
// trigger, so click still navigates (via hx-boost / the href). Runs while
// hrefs are still /w/ routes — that is where the card's source is read.
// Listings, tag pages, anchors, and external links are not wrapped.
// Returns the fragment unchanged on any parse failure.
func previewize(fragment string) string {
	ctx := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragment(strings.NewReader(fragment), ctx)
	if err != nil {
		return fragment
	}
	for _, n := range nodes {
		previewizeNode(n)
	}
	var buf bytes.Buffer
	for _, n := range nodes {
		if err := html.Render(&buf, n); err != nil {
			return fragment
		}
	}
	return buf.String()
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
	addr, _, ok := previewableAnchor(n)
	if !ok {
		return
	}
	wrapWithPreview(n, previewURL(domain.Ref{World: addr.World, Path: addr.Value}))
}

// previewableAnchor reports whether an anchor points at a real document (the
// only previewable target): an in-app /w/ doc route that is not a listing.
func previewableAnchor(n *html.Node) (paneAddr, string, bool) {
	for _, attr := range n.Attr {
		if attr.Key != "href" {
			continue
		}
		addr, frag, ok := paneAddrFromRoute(attr.Val)
		if !ok || addr.Kind != paneDoc || strings.HasSuffix(addr.Value, "/") {
			return paneAddr{}, "", false
		}
		return addr, frag, true
	}
	return paneAddr{}, "", false
}

// wrapWithPreview reparents anchor under a preview-host span, appends the
// (initially empty) preview-card span, and adds the htmx hover attributes.
func wrapWithPreview(anchor *html.Node, src string) {
	parent := anchor.Parent
	if parent == nil {
		return
	}
	host := &html.Node{Type: html.ElementNode, DataAtom: atom.Span, Data: "span",
		Attr: []html.Attribute{{Key: "class", Val: "preview-host"}}}
	parent.InsertBefore(host, anchor)
	parent.RemoveChild(anchor)
	host.AppendChild(anchor)

	anchor.Attr = append(anchor.Attr,
		html.Attribute{Key: "hx-get", Val: src},
		html.Attribute{Key: "hx-trigger", Val: "mouseenter delay:300ms once"},
		html.Attribute{Key: "hx-target", Val: "next .preview-card"},
		html.Attribute{Key: "hx-swap", Val: "innerHTML"},
	)
	card := &html.Node{Type: html.ElementNode, DataAtom: atom.Span, Data: "span",
		Attr: []html.Attribute{{Key: "class", Val: "preview-card"}, {Key: "role", Val: "tooltip"}}}
	host.AppendChild(card)
}

// previewSnippet extracts the opening prose of a rendered document for the
// card: the text of the first paragraph, trimmed to previewSnippetLen.
func previewSnippet(htmlStr string) string {
	ctx := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragment(strings.NewReader(htmlStr), ctx)
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
