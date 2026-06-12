package markdown

import (
	"github.com/yuin/goldmark"
	gast "github.com/yuin/goldmark/ast"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// sidenotes is the goldmark extension behind the Tufte margin (ADR 0005
// decision 8): authors and agents write standard footnote syntax ([^1] plus
// a [^1]: definition), and the AST transform moves each note's inline
// content to its reference point as a margin span. CSS floats the span into
// the margin and numbers both ends with a counter.
//
// Only footnotes that translate cleanly become sidenotes: a single-paragraph
// definition referenced exactly once. A multi-paragraph note has no honest
// single-span rendering, and a note referenced twice would need its content
// duplicated into two margin positions — both stay classic footnotes at the
// document foot. Degrading to footnotes is the contract (the no-CSS/agent
// view is footnotes either way), not an error.
type sidenotes struct{}

func (sidenotes) Extend(m goldmark.Markdown) {
	m.Parser().AddOptions(parser.WithASTTransformers(
		// Priority 1000: after the footnote extension's own transformer
		// (999), which assigns indexes/ref counts and attaches the
		// footnote list to the document.
		util.Prioritized(sidenoteTransformer{}, 1000),
	))
	m.Renderer().AddOptions(renderer.WithNodeRenderers(
		util.Prioritized(sidenoteRenderer{}, 450),
	))
}

// sidenoteNode is an inline node holding a footnote's inline content at its
// reference point.
type sidenoteNode struct {
	gast.BaseInline
}

var kindSidenote = gast.NewNodeKind("Sidenote")

func (n *sidenoteNode) Kind() gast.NodeKind { return kindSidenote }

func (n *sidenoteNode) Dump(source []byte, level int) {
	gast.DumpHelper(n, source, level, nil, nil)
}

type sidenoteTransformer struct{}

func (sidenoteTransformer) Transform(doc *gast.Document, _ text.Reader, _ parser.Context) {
	var list *extast.FootnoteList
	for c := doc.FirstChild(); c != nil; c = c.NextSibling() {
		if l, ok := c.(*extast.FootnoteList); ok {
			list = l
			break
		}
	}
	if list == nil {
		return
	}

	notes := map[int]*extast.Footnote{}
	for c := list.FirstChild(); c != nil; c = c.NextSibling() {
		if fn, ok := c.(*extast.Footnote); ok {
			notes[fn.Index] = fn
		}
	}

	// Collect first, mutate after: replacing nodes mid-walk invalidates the
	// walker's position.
	var links []*extast.FootnoteLink
	_ = gast.Walk(doc, func(n gast.Node, entering bool) (gast.WalkStatus, error) {
		if entering {
			if link, ok := n.(*extast.FootnoteLink); ok {
				links = append(links, link)
			}
		}
		return gast.WalkContinue, nil
	})

	for _, link := range links {
		if link.RefCount > 1 {
			continue
		}
		fn := notes[link.Index]
		if fn == nil || fn.ChildCount() != 1 || !gast.IsParagraph(fn.FirstChild()) {
			continue
		}
		para := fn.FirstChild()

		side := &sidenoteNode{}
		for c := para.FirstChild(); c != nil; {
			next := c.NextSibling()
			// The footnote transformer appended a backlink to the
			// paragraph; there is no foot entry to link back to anymore.
			if _, isBacklink := c.(*extast.FootnoteBacklink); !isBacklink {
				side.AppendChild(side, c)
			}
			c = next
		}
		link.Parent().ReplaceChild(link.Parent(), link, side)
		list.RemoveChild(list, fn)
		list.Count--
	}

	if list.FirstChild() == nil {
		doc.RemoveChild(doc, list)
	}
}

type sidenoteRenderer struct{}

func (r sidenoteRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(kindSidenote, r.render)
}

// render wraps the note's inline children. The empty <sup> is the in-text
// anchor; CSS counters number it and the span identically, so the pairing
// needs no ids — nothing for the sanitizer to police.
func (sidenoteRenderer) render(w util.BufWriter, _ []byte, _ gast.Node, entering bool) (gast.WalkStatus, error) {
	if entering {
		_, _ = w.WriteString(`<sup class="sidenote-number"></sup><span class="sidenote">`)
	} else {
		_, _ = w.WriteString(`</span>`)
	}
	return gast.WalkContinue, nil
}
