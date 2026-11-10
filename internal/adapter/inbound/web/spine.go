package web

import (
	"path"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// spineVM is what a collapsed pane carries beside its title and number (ADR
// 0006 §2, amended). Nil for a pane whose title already says everything.
type spineVM struct {
	Kind string       // the document's OKF type; "" when the title already says it
	Foot string       // where the pane lives, or what it holds
	Peek *spinePeekVM // a document's card and thumb index; nil for any other pane
}

// spinePeekVM is a collapsed document's card: where it is, how it opens, and
// the sections the thumb index marks.
type spinePeekVM struct {
	Modified string // the day of the last write; "" when the world did not say
	Lede     string
	Sections []spineSection
}

// spineSection is one second-level heading and how far into the text it sits.
type spineSection struct {
	Title string
	At    int // percent of the document's text that precedes the heading
}

// docSpine builds a collapsed document pane's spine. A listing or tag page has
// nothing to read through, so it has none.
func docSpine(addr paneAddr, doc domain.Document) *spineVM {
	if !isDocPane(addr) {
		return nil
	}
	peek := spinePeek(doc.HTML)
	peek.Modified = modifiedDay(doc.Modified)
	return &spineVM{Kind: doc.Type, Foot: path.Dir(doc.Path), Peek: peek}
}

// modifiedDay cuts a catalog timestamp to its day: the card says how fresh a
// document is, not when to the second. An unparsed value passes through.
func modifiedDay(modified string) string {
	stamp, err := time.Parse(time.RFC3339, modified)
	if err != nil {
		return modified
	}
	return stamp.Format(time.DateOnly)
}

// spinePeek reads the opening line and the section outline out of a rendered
// document. Best-effort: unparseable content leaves the card with neither.
func spinePeek(rendered string) *spinePeekVM {
	nodes, err := parseFragment(rendered)
	if err != nil {
		return &spinePeekVM{}
	}
	outline := &outlineWalk{}
	for _, node := range nodes {
		outline.visit(node)
	}
	return &spinePeekVM{Lede: openingSnippet(nodes), Sections: outline.sections()}
}

// outlineWalk measures how much text precedes each second-level heading.
type outlineWalk struct {
	textLen  int
	headings []outlineHeading
}

type outlineHeading struct {
	title  string
	offset int // text bytes before the heading
}

func (w *outlineWalk) visit(node *html.Node) {
	if node.Type == html.TextNode {
		w.textLen += len(node.Data)
		return
	}
	if node.Type == html.ElementNode && node.DataAtom == atom.H2 {
		if title := textContent(node); title != "" {
			w.headings = append(w.headings, outlineHeading{title: title, offset: w.textLen})
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		w.visit(child)
	}
}

func (w *outlineWalk) sections() []spineSection {
	if w.textLen == 0 {
		return nil
	}
	sections := make([]spineSection, len(w.headings))
	for i, heading := range w.headings {
		sections[i] = spineSection{Title: heading.title, At: heading.offset * 100 / w.textLen}
	}
	return sections
}

// numberPanes gives each pane its place on the trail, which a spine prints.
// One pass after the pane loop, so no builder repeats it.
func numberPanes(panes []paneVM) {
	for i := range panes {
		panes[i].Ordinal = i + 1
	}
}
