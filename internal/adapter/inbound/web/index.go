package web

import (
	"bytes"
	"context"
	"strings"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// The rich directory index (ADR 0006 §5): a raw listing is a bare ls of
// filenames; this enriches each document row with what the catalog knows — the
// title (primary), the *.md filename (mono secondary), a status badge, and an
// orphan tag — so there is one name per document across the index and the map,
// killing the filename↔title split. Subdirectory rows are left untouched. It
// runs after rewriteLinks (hrefs are /w/ doc routes, decodable here) and before
// previewize/trailize.

// richIndex enriches a rendered listing fragment with catalog metadata for the
// world. Best-effort: a catalog read failure (or an unreadable world) leaves
// the listing as a plain ls — the index degrades, never errors.
func (h *ReadingHandler) richIndex(ctx context.Context, world, fragment string) string {
	entries, err := h.reading.NameIndex(ctx, "world", world)
	if err != nil || len(entries) == 0 {
		return fragment
	}
	byPath := make(map[string]domain.IndexEntry, len(entries))
	for _, e := range entries {
		byPath[e.Path] = e
	}
	return indexify(fragment, byPath)
}

func indexify(fragment string, byPath map[string]domain.IndexEntry) string {
	ctxNode := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragment(strings.NewReader(fragment), ctxNode)
	if err != nil {
		return fragment
	}
	for _, n := range nodes {
		indexifyNode(n, byPath)
	}
	var buf bytes.Buffer
	for _, n := range nodes {
		if err := html.Render(&buf, n); err != nil {
			return fragment
		}
	}
	return buf.String()
}

func indexifyNode(n *html.Node, byPath map[string]domain.IndexEntry) {
	// Recurse first, capturing the next sibling before any insertion mutates the
	// tree (the inserts reparent siblings, changing n.NextSibling).
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		indexifyNode(c, byPath)
		c = next
	}
	if n.Type != html.ElementNode || n.DataAtom != atom.A {
		return
	}
	var href string
	for _, a := range n.Attr {
		if a.Key == "href" {
			href = a.Val
		}
	}
	addr, _, ok := paneAddrFromRoute(href)
	if !ok || addr.Kind != paneDoc || strings.HasSuffix(addr.Value, "/") {
		return // a subdirectory row or a non-document link — leave it as is
	}
	e, ok := byPath[addr.Value]
	if !ok {
		return // not in the catalog (e.g. an untitled file) — leave the filename
	}
	// The title becomes the row's primary text; the filename, status, and orphan
	// tag follow it (mono secondary + badges) — door affordances over a bare ls.
	setNodeText(n, e.Title)
	anchor := insertAfter(n, spanNode("idx-file", baseFile(addr.Value)))
	if e.Status != "" {
		anchor = insertAfter(anchor, spanNode("status status-"+e.Status, e.Status))
	}
	if e.Orphan {
		insertAfter(anchor, spanNode("idx-orphan", "orphan"))
	}
}

// baseFile is a path's final segment (the *.md filename).
func baseFile(path string) string {
	return path[strings.LastIndex(path, "/")+1:]
}

// setNodeText replaces a node's children with a single text node.
func setNodeText(n *html.Node, text string) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		n.RemoveChild(c)
		c = next
	}
	n.AppendChild(&html.Node{Type: html.TextNode, Data: text})
}

// spanNode builds <span class="…">text</span>.
func spanNode(class, text string) *html.Node {
	s := &html.Node{
		Type: html.ElementNode, Data: "span", DataAtom: atom.Span,
		Attr: []html.Attribute{{Key: "class", Val: class}},
	}
	s.AppendChild(&html.Node{Type: html.TextNode, Data: text})
	return s
}

// insertAfter inserts node immediately after ref among its parent's children,
// returning node so inserts can chain.
func insertAfter(ref, node *html.Node) *html.Node {
	p := ref.Parent
	if p == nil {
		return ref
	}
	if ref.NextSibling == nil {
		p.AppendChild(node)
	} else {
		p.InsertBefore(node, ref.NextSibling)
	}
	return node
}
