// Package markdown is the outbound adapter that implements port.Renderer with
// goldmark + bluemonday. goldmark mirrors the rest of the project (the TUI uses
// Glamour for terminals; the web uses goldmark for HTML); bluemonday closes the
// markdown-to-HTML XSS path on org-authored content.
package markdown

import (
	"bytes"
	"regexp"
	"sort"
	"strings"

	"github.com/alecthomas/chroma/v2"
	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/gohugoio/hugo-goldmark-extensions/passthrough"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus-library/internal/core/port"
	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	emoji "github.com/yuin/goldmark-emoji"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/extension"
	alertcallouts "github.com/zmtcreative/gm-alert-callouts"
)

// Renderer renders GFM markdown to sanitized HTML.
type Renderer struct {
	md     goldmark.Markdown
	policy *bluemonday.Policy
}

// compile-time check that Renderer satisfies the outbound port.
var _ port.Renderer = (*Renderer)(nil)

// chromaClasses matches exactly the class attribute values chroma's
// HTML formatter can emit in classes mode. The alternation is built
// from chroma.StandardTypes — the same table the formatter reads its
// class names from — so the allowlist is exact by construction: every
// token code (`k`, `nf`, `s2`, …) and structural class (`chroma`,
// `line`, `cl`, line-number variants) passes, and nothing else does.
// A shape-based pattern was rejected in review: `[a-z]{1,3}` also
// matched author-controlled non-chroma tokens like `nav` and `btn`.
// Space-separated combinations of allowed atoms are accepted.
var chromaClasses = chromaClassPattern()

func chromaClassPattern() *regexp.Regexp {
	names := make([]string, 0, len(chroma.StandardTypes))
	for _, name := range chroma.StandardTypes {
		if name != "" {
			names = append(names, regexp.QuoteMeta(name))
		}
	}
	// Longest-first so alternation can't shadow longer names sharing a
	// prefix (regexp alternation is leftmost-match); sorted for
	// determinism.
	sort.Slice(names, func(i, j int) bool {
		if len(names[i]) != len(names[j]) {
			return len(names[i]) > len(names[j])
		}
		return names[i] < names[j]
	})
	atom := "(?:" + strings.Join(names, "|") + ")"
	return regexp.MustCompile("^" + atom + "(?: " + atom + ")*$")
}

// sidenoteClasses matches what the sidenote extension emits: the in-text
// anchor (<sup class="sidenote-number">) and the margin span
// (<span class="sidenote">). CSS counters pair them — no ids involved.
var sidenoteClasses = regexp.MustCompile(`^sidenote(?:-number)?$`)

// footnoteClasses / footnoteIDs admit the goldmark footnote fallback for
// notes that don't become sidenotes (multi-paragraph, or referenced more
// than once): ref/backref anchors and the foot-of-document list. The id
// shapes are exactly what the footnote renderer emits (fn:N, fnref:N, and
// fnref:N:M for later references), so author-controlled ids can't ride in
// (DOM clobbering stays closed).
var (
	footnoteClasses = regexp.MustCompile(`^footnote-(?:ref|backref)$|^footnotes$`)
	footnoteIDs     = regexp.MustCompile(`^fn(?:ref)?:[0-9]+(?::[0-9]+)?$`)
	footnoteHrefs   = regexp.MustCompile(`^#fn(?:ref)?:[0-9]+(?::[0-9]+)?$`)
)

// calloutClasses matches what gm-alert-callouts emits: the wrapper div
// (`callout callout-<kind>` — kind derives from the author's [!KIND]
// marker, so it's bounded rather than enumerated; unknown kinds render
// with the default style) and the fixed structural classes. CSS in
// page.html styles the five GFM kinds; anything else inherits the
// neutral .callout look.
var calloutClasses = regexp.MustCompile(`^callout(?: callout-[a-z0-9-]{1,32})?$|^callout-(?:title|title-text|body)$`)

// alertIcons replaces the extension's default inline-SVG icon set with
// emoji. Deliberate: SVG through the sanitizer would mean allowlisting
// a dozen svg/path attributes (a real widening of the XSS surface), and
// ADR 0003's zero-JS/zero-asset posture favors text. The five GFM
// alert kinds; unknown kinds fall back to the note icon.
var alertIcons = map[string]string{
	"note":      "ℹ️",
	"tip":       "💡",
	"important": "❗",
	"warning":   "⚠️",
	"caution":   "🛑",
}

// NewRenderer builds a GFM renderer with a UGC sanitization policy.
//
// Syntax highlighting is server-side (chroma via goldmark-highlighting,
// zero JS) in CLASSES mode, never inline styles: bluemonday strips
// `style` attributes, and widening the policy to allow them would
// reopen the CSS injection surface the sanitizer exists to close.
// Colors come from /static/chroma.css; the policy admits only
// chroma-shaped class names (chromaClasses) on the elements chroma
// emits them on.
func NewRenderer() *Renderer {
	policy := bluemonday.UGCPolicy()
	policy.AllowAttrs("class").Matching(chromaClasses).OnElements("pre", "code", "span")
	// chroma marks highlighted <pre> blocks tabindex="0" so keyboard
	// users can focus + scroll an overflowing block. Pin to exactly 0 —
	// positive tabindexes reorder the page's tab sequence.
	policy.AllowAttrs("tabindex").Matching(regexp.MustCompile(`^0$`)).OnElements("pre")
	// GFM alert structure (div wrapper + title/body divs, classed title
	// paragraph). data-callout is NOT admitted — CSS keys on the classes.
	policy.AllowAttrs("class").Matching(calloutClasses).OnElements("div", "p")
	// Mermaid fences: chroma has no mermaid lexer, so the block falls
	// through to goldmark's plain renderer carrying language-mermaid —
	// the marker the mermaid island keys on (islands.js). Exactly this
	// one language class; other unlexed languages don't need a client
	// hook and stay classless.
	policy.AllowAttrs("class").Matching(regexp.MustCompile(`^language-mermaid$`)).OnElements("code")
	// Sidenotes (ADR 0005 decision 8): the margin span and its in-text
	// anchor, numbered by CSS counters.
	policy.AllowAttrs("class").Matching(sidenoteClasses).OnElements("span", "sup")
	// Footnote fallback (notes that don't qualify as sidenotes): goldmark's
	// ref/backref anchors need fn-shaped ids and fragment hrefs to jump
	// between text and foot.
	policy.AllowAttrs("class").Matching(footnoteClasses).OnElements("a", "div")
	policy.AllowAttrs("id").Matching(footnoteIDs).OnElements("sup", "li")
	policy.AllowAttrs("href").Matching(footnoteHrefs).OnElements("a")
	return &Renderer{
		md: goldmark.New(goldmark.WithExtensions(
			extension.GFM,
			// Footnote syntax is the sidenote channel: the sidenotes
			// extension (below, transformer priority 1000) rewrites
			// qualifying notes into margin spans after the footnote
			// transformer (999) has shaped the AST.
			extension.Footnote,
			sidenotes{},
			highlighting.NewHighlighting(
				highlighting.WithFormatOptions(chromahtml.WithClasses(true)),
			),
			// GFM alerts (> [!NOTE] …): emoji icon set, see alertIcons.
			alertcallouts.NewAlertCallouts(alertcallouts.WithIcons(alertIcons)),
			// :shortcode: emoji → unicode characters (plain text, nothing
			// for the sanitizer to strip).
			emoji.Emoji,
			// Math passthrough: protect TeX between the delimiters from
			// markdown processing (underscores, backslashes) and emit it
			// verbatim — the KaTeX island (islands.js) renders it
			// client-side and the raw TeX is the no-JS degradation.
			// Single-$ inline math is deliberately absent: "$5 and $10"
			// false-positives; authors write \( … \) for inline.
			passthrough.New(passthrough.Config{
				InlineDelimiters: []passthrough.Delimiters{{Open: `\(`, Close: `\)`}},
				BlockDelimiters: []passthrough.Delimiters{
					{Open: "$$", Close: "$$"},
					{Open: `\[`, Close: `\]`},
				},
			}),
		)),
		policy: policy,
	}
}

// Render renders markdown then sanitizes the result. Sanitize always runs after
// render — never trust the HTML goldmark emits from untrusted source.
//
// A leading YAML frontmatter fence is split off first (splitFrontmatter) and
// parsed into display properties rather than rendered as garbled text — the
// margin shows it friendly (ADR 0005 decision 7) while demarkus's
// out-of-band metadata stays the authoritative catalog channel.
func (r *Renderer) Render(markdown string) (domain.Rendered, error) {
	fence, body := splitFrontmatter(markdown)
	var buf bytes.Buffer
	if err := r.md.Convert([]byte(body), &buf); err != nil {
		return domain.Rendered{}, err
	}
	return domain.Rendered{
		HTML:       r.policy.Sanitize(buf.String()),
		Properties: parseFrontmatter(fence),
	}, nil
}
