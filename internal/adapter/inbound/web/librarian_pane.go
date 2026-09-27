package web

// The librarian pane (Phase 4, plans/phase-4-ai-librarian.md): the reader's
// conversation with the AI librarian as a canvas pane — kind `a`, joining the
// trail so reading and asking share one spatial state. The transcript renders
// server-side from port.Librarian.History; answers are markdown rendered
// through the same pipeline as documents (Preview → rewriteLinks → previewize
// → trailize), so mark:// citations become trail-continuing links.
//
// Asking is a real CSRF'd form POST (ADR 0003): with htmx it returns an
// exchange fragment whose SSE block streams the answer live; without JS the
// handler runs the ask to completion and redirects back to the trail (PRG),
// where the pane re-renders the finished exchange from History.

import (
	"context"
	"fmt"
	"html/template"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus-library/internal/core/port"
	"golang.org/x/net/html"
)

// maxQuestionBytes bounds one question (plan D7) — well under the global
// body limit; a question is a question, not a document.
const maxQuestionBytes = 4 * 1024

// librarianReader is what the librarian needs from the reading core: the
// renderer documents go through (so an answer is rendered the same way) and
// the cached reads that build the reader's context without spending the
// focused-live budget.
type librarianReader interface {
	Preview(markdown string) (domain.Rendered, error)
	OpenCached(ctx context.Context, world, path string) (domain.Document, error)
}

// librarianPanes builds everything the librarian contributes to a rendered
// page: its canvas pane, the answer HTML, and the reader-context block an ask
// carries. The canvas holds one (it composes every pane kind) and so does
// LibrarianHandler, so the ports and the render pipeline are declared once.
type librarianPanes struct {
	lib          port.Librarian // nil = not configured; the pane says so and asks are rejected
	reader       librarianReader
	identity     librarianIdentity // carries the room's Terms too
	defaultWorld string
}

// enabled reports whether a librarian is on duty — the nav door and the ask
// form hang off it.
func (h librarianPanes) enabled() bool { return h.lib != nil }

// librarianAskVM feeds the pane's ask form: the current trail context rides
// as hidden fields so the POST can rebuild post-ask URLs and the stream can
// trailize answer links.
type librarianAskVM struct {
	TrailRest string   // the /t/* remainder for the current trail
	Idx       int      // this pane's index on the trail
	Focus     int      // the reader's URL focus — the pane their attention is on
	Notice    string   // busy/error line rendered above the form (no-JS PRG)
	Starters  []string // suggested first questions, until the conversation begins
	Autofocus bool     // the pane holds the reader's focus: the cursor goes to the ask box
}

// librarianStepVM is one trace line: what the librarian did, and a
// trail-continuing link to the document it touched (Href "" when none).
type librarianStepVM struct {
	Text  string
	Href  string
	Label string
}

// librarianSourceVM is one document an answer was grounded in.
type librarianSourceVM struct {
	Title string
	Href  string
	Label string
}

// librarianExchangeVM renders one transcript exchange; Answer is the
// document-pipeline-rendered HTML. A live exchange (htmx ask response)
// carries StreamURL instead — the SSE block that fills in as the librarian
// works.
type librarianExchangeVM struct {
	Question  string
	Answer    template.HTML
	Steps     []librarianStepVM
	Sources   []librarianSourceVM
	Stopped   bool
	StreamURL string
}

// librarianPaneVM is the pane template's librarian branch data.
type librarianPaneVM struct {
	Enabled   bool
	Exchanges []librarianExchangeVM
	Ask       *librarianAskVM // nil unless the pane is expanded and enabled
}

// pane builds the librarian pane: transcript from History, rendered like any
// pane body, ask form when expanded. No world read, never errors — a
// librarian problem is a notice, not a tombstone.
func (h librarianPanes) pane(c *echo.Context, t trail, i int) paneVM {
	focused := i == t.Focus
	mode := "spine"
	switch {
	case focused:
		mode = "focused"
	case i == t.Focus-1:
		mode = "body"
	}
	name := h.identity.name(c.Request().Context())
	// The head names the librarian; the pane has no title heading of its own,
	// so the conversation starts right under the head.
	vm := paneVM{
		Mode:     mode,
		Kind:     paneLibrarian,
		FocusURL: trailURL(trailFocused(t, i)),
		Title:    name,
		World:    name,
	}
	if mode == "spine" {
		return vm
	}

	lp := librarianPaneVM{Enabled: h.lib != nil}
	if h.lib != nil {
		for _, ex := range h.lib.History(conversationKey(c)) {
			lp.Exchanges = append(lp.Exchanges, h.exchangeVM(ex, t, i))
		}
		// The ask form rides every EXPANDED librarian pane, focused or not:
		// reading a cited document focuses the doc pane, and that is exactly
		// when the follow-up question comes — losing the ask bar there would
		// force a re-focus round trip. Only the collapsed spine drops it.
		lp.Ask = &librarianAskVM{
			TrailRest: strings.TrimPrefix(trailBasePath(t), "/t/"),
			Idx:       i,
			Focus:     t.Focus,
			Notice:    c.QueryParam("notice"),
			Autofocus: focused,
		}
		if len(lp.Exchanges) == 0 {
			lp.Ask.Starters = h.starters(t, t.Focus)
		}
	}
	vm.Librarian = &lp
	return vm
}

// exchangeVM renders one finished exchange for pane i of trail t.
func (h librarianPanes) exchangeVM(ex domain.LibrarianExchange, t trail, i int) librarianExchangeVM {
	vm := librarianExchangeVM{Question: ex.Question, Stopped: ex.Stopped}
	if ex.Answer != "" {
		vm.Answer = h.renderAnswer(ex.Answer, t, i)
	}
	for _, step := range ex.Steps {
		vm.Steps = append(vm.Steps, stepVM(step, t, i))
	}
	for _, src := range ex.Sources {
		vm.Sources = append(vm.Sources, sourceVM(src, t, i))
	}
	return vm
}

// stepVM links a trace step's document so the reader can follow the
// librarian's path from pane i.
func stepVM(step domain.LibrarianStep, t trail, i int) librarianStepVM {
	vm := librarianStepVM{Text: step.Text}
	if step.Ref.Path != "" {
		vm.Href, vm.Label = librarianDocLink(step.Ref, t, i), step.Ref.World+step.Ref.Path
	}
	return vm
}

// sourceVM links one grounding document from pane i.
func sourceVM(src domain.LibrarianSource, t trail, i int) librarianSourceVM {
	return librarianSourceVM{Title: src.Title, Href: librarianDocLink(src.Ref, t, i), Label: src.World + src.Path}
}

// librarianDocLink is the trail-continuing URL for a document the librarian
// names: the click algebra from the librarian pane, like a cited link.
func librarianDocLink(ref domain.Ref, t trail, i int) string {
	return trailURL(trailAfterClick(t, i, paneAddr{Kind: paneDoc, World: ref.World, Value: ref.Path}))
}

// starters suggests first questions: about the document in view when there
// is one, about the collection otherwise.
func (h librarianPanes) starters(t trail, focus int) []string {
	if contextDoc(t, focus) >= 0 {
		return []string{
			"Summarize this document",
			"What links here, and what does it link to?",
			"What else covers this subject?",
		}
	}
	return []string{
		"What is in this " + h.identity.terms.UniverseLower() + "?",
		"Where should I start reading?",
		"Which documents matter most?",
	}
}

// contextDoc is the index of the document the reader is asking about: the
// focused pane when it is a document, else — when the librarian itself holds
// focus — the nearest document to its left, the one open beside the
// conversation. -1 when no document is in view.
func contextDoc(t trail, focus int) int {
	if focus < 0 || focus >= len(t.Panes) {
		return -1
	}
	isDoc := func(p paneAddr) bool { return p.Kind == paneDoc && !domain.IsListingPath(p.Value) }
	if isDoc(t.Panes[focus]) {
		return focus
	}
	if t.Panes[focus].Kind != paneLibrarian {
		return -1
	}
	for i := focus - 1; i >= 0; i-- {
		if isDoc(t.Panes[i]) {
			return i
		}
	}
	return -1
}

// renderAnswer runs an answer's markdown through the document pipeline:
// render + sanitize (the model's output is untrusted input), resolve links to
// in-app routes, wrap hover previews, and trailize so a cited document
// continues the trail from the librarian pane.
func (h librarianPanes) renderAnswer(markdown string, t trail, i int) template.HTML {
	rendered, err := h.reader.Preview(markdown)
	if err != nil {
		// Render failure degrades to escaped plain text — never raw model
		// output into the page.
		return template.HTML("<pre>" + template.HTMLEscapeString(markdown) + "</pre>") //nolint:gosec // explicitly escaped one line up
	}
	content, _ := rewriteLinks(rendered.HTML, h.defaultWorld, "/")
	return template.HTML(trailizeLinks(previewize(content), t, i, false)) //nolint:gosec // sanitized by Preview; the passes only rewrite/wrap links
}

// conversationKey names the reader's conversation: the session cookie in
// broker mode (the conversation follows the login), one shared local key in
// quic mode. The key is server-side only — it never appears in a URL.
func conversationKey(c *echo.Context) string {
	if cookie, err := c.Cookie(sessionCookie); err == nil && cookie.Value != "" {
		return cookie.Value
	}
	return "local"
}

// trailContextBudget caps the focused document's text fed with each ask —
// token economy over completeness; the librarian can open the full document
// itself, visibly in the trace.
const trailContextBudget = 8 * 1024

// trailContext renders the reader's current view for the librarian (plan D5:
// trail = context): every pane's label, plus the focused document's text
// extracted from its CACHED render — zero extra world reads. Best-effort
// throughout: a cold cache just means less context, never a failed ask.
func (h librarianPanes) trailContext(ctx context.Context, t trail, focus int) string {
	var b strings.Builder
	b.WriteString("<reader-context>\nThe reader's open panes (their trail), oldest first:\n")
	for i, p := range t.Panes {
		b.WriteString("- ")
		b.WriteString(h.paneContextLabel(ctx, p))
		if i == focus {
			b.WriteString("   <- the reader's focus")
		}
		b.WriteByte('\n')
	}
	if at := contextDoc(t, focus); at >= 0 {
		fa := t.Panes[at]
		if doc, err := h.reader.OpenCached(ctx, fa.World, fa.Value); err == nil {
			which := "The focused document"
			if at != focus {
				which = "The document open beside this conversation"
			}
			text := truncateRunes(neutralizeContextTags(htmlText(doc.HTML)), trailContextBudget)
			fmt.Fprintf(&b, "\n%s (mark://%s%s — %q) as the reader sees it:\n\"\"\"\n%s\n\"\"\"\n", which, fa.World, fa.Value, neutralizeContextTags(doc.Title), text)
		}
	}
	b.WriteString("</reader-context>")
	return b.String()
}

// paneContextLabel names one pane for the context block.
func (h librarianPanes) paneContextLabel(ctx context.Context, p paneAddr) string {
	switch p.Kind {
	case paneLibrarian:
		return "this librarian conversation"
	case paneFloor:
		if p.World == "" {
			return "the " + h.identity.terms.UniverseLower() + " floor"
		}
		return "map of world " + p.World
	case paneGraph:
		return "graph of mark://" + p.World + p.Value
	case paneTag:
		return "tag page #" + p.Value + " in world " + p.World
	default:
		label := "mark://" + p.World + p.Value
		if doc, err := h.reader.OpenCached(ctx, p.World, p.Value); err == nil && doc.Title != "" {
			label += " — " + strconv.Quote(neutralizeContextTags(doc.Title))
		}
		return label
	}
}

// contextTagPattern matches any spelling of the reader-context wrapper tag
// inside DOCUMENT-derived text. Document content is untrusted for prompt
// structure: a doc containing a literal </reader-context> could close the
// wrapper early and speak with the reader's voice.
var contextTagPattern = regexp.MustCompile(`(?i)</?\s*reader-context\s*>?`)

// neutralizeContextTags defangs wrapper-tag lookalikes in text headed into
// the <reader-context> block, keeping the structural markers unambiguous.
func neutralizeContextTags(s string) string {
	return contextTagPattern.ReplaceAllString(s, "[reader-context tag removed]")
}

// htmlText flattens rendered HTML to readable plain text for the model:
// text nodes joined, block elements separated by newlines, whitespace
// collapsed. Fed only sanitized library-rendered HTML.
func htmlText(fragment string) string {
	nodes, err := parseFragment(fragment)
	if err != nil {
		return ""
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			return
		}
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style":
				return
			case "p", "li", "h1", "h2", "h3", "h4", "h5", "h6", "tr", "pre", "blockquote", "br", "div":
				b.WriteByte('\n')
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	for _, n := range nodes {
		walk(n)
	}
	// Collapse runs of blank lines and intra-line whitespace.
	lines := strings.Split(b.String(), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// truncateRunes cuts s at limit bytes without splitting a UTF-8 rune,
// appending a truncation note when anything was cut.
func truncateRunes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n[… truncated — the librarian can open the document for the rest]"
}
