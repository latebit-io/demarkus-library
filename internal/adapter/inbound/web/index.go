package web

import (
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

// richIndex enriches a rendered listing with the world's catalog metadata,
// read live for the focused pane and cached otherwise. Best-effort: a failed
// read leaves the plain ls; the index degrades, never errors.
func (h *ReadingHandler) richIndex(ctx context.Context, world, fragment string, live bool) string {
	var entries []domain.IndexEntry
	var err error
	if live {
		entries, err = h.reading.NameIndex(ctx, "world", world)
	} else {
		entries, err = h.reading.NameIndexCached(ctx, world)
	}
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
	pass := &indexPass{byPath: byPath, common: commonStatus(byPath)}
	out, err := rewriteFragment(fragment, pass.visit)
	if err != nil {
		return fragment
	}
	if pass.quieted > 0 {
		// Rows at the world's usual status carry no badge; say so once.
		out += `<p class="idx-note">Unmarked documents are ` + html.EscapeString(pass.common) + `.</p>`
	}
	return out
}

// indexPass enriches one listing. common is the world's most frequent status:
// a badge on nearly every row is noise, so rows at it go unmarked and quieted
// counts them.
type indexPass struct {
	byPath  map[string]domain.IndexEntry
	common  string
	quieted int
}

// commonStatus is the status most of the world's documents carry, ties broken
// by name so the choice is stable; "" when none is set.
func commonStatus(byPath map[string]domain.IndexEntry) string {
	counts := map[string]int{}
	for _, e := range byPath {
		if e.Status != "" {
			counts[e.Status]++
		}
	}
	common := ""
	for status, n := range counts {
		if n > counts[common] || (n == counts[common] && status < common) {
			common = status
		}
	}
	return common
}

func (p *indexPass) visit(n *html.Node) {
	// Recurse first, capturing the next sibling before any insertion mutates the
	// tree (the inserts reparent siblings, changing n.NextSibling).
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		p.visit(c)
		c = next
	}
	if n.Type != html.ElementNode || n.DataAtom != atom.A {
		return
	}
	addr, ok := anchorDocAddr(n)
	if !ok || domain.IsListingPath(addr.Value) {
		return // a subdirectory row or a non-document link — leave it as is
	}
	e, ok := p.byPath[addr.Value]
	if !ok {
		return // not in the catalog (e.g. an untitled file) — leave the filename
	}
	// The title becomes the row's primary text; the filename, status, and orphan
	// tag follow it (mono secondary + badges) — door affordances over a bare ls.
	setNodeText(n, e.Title)
	anchor := insertAfter(n, spanNode("idx-file", baseFile(addr.Value)))
	switch e.Status {
	case "":
	case p.common:
		p.quieted++
	default:
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
