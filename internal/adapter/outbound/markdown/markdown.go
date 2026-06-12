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
	"github.com/latebit-io/demarkus-library/internal/core/port"
	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	emoji "github.com/yuin/goldmark-emoji"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
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
	return &Renderer{
		md: goldmark.New(goldmark.WithExtensions(
			extension.GFM,
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
// A leading YAML frontmatter block is stripped first: demarkus carries
// metadata out of band, but worlds contain bodies that open with a ---…---
// metadata fence anyway (publishers that hand-wrote frontmatter, or
// republished a fetched document verbatim, header included). goldmark would
// render that fence as garbled text; to a reader it is metadata, not
// content.
func (r *Renderer) Render(markdown string) (string, error) {
	var buf bytes.Buffer
	if err := r.md.Convert([]byte(stripFrontmatter(markdown)), &buf); err != nil {
		return "", err
	}
	return r.policy.Sanitize(buf.String()), nil
}

// stripFrontmatter drops a leading YAML frontmatter block: a first line of
// exactly "---", closed by a line of exactly "---" or "...". Anything that
// does not match precisely (no opener, no closer, content before the fence)
// is returned unchanged — a thematic break mid-document is content, and an
// unclosed fence is safer rendered than silently swallowing the whole body.
func stripFrontmatter(markdown string) string {
	rest, ok := strings.CutPrefix(markdown, "---\n")
	if !ok {
		if rest, ok = strings.CutPrefix(markdown, "---\r\n"); !ok {
			return markdown
		}
	}
	for off := 0; off < len(rest); {
		lineEnd := strings.IndexByte(rest[off:], '\n')
		if lineEnd < 0 {
			// Last line has no trailing newline: a closer here means the
			// document was nothing but frontmatter.
			if line := strings.TrimSuffix(rest[off:], "\r"); line == "---" || line == "..." {
				return ""
			}
			break // unclosed fence — leave the document alone
		}
		line := strings.TrimSuffix(rest[off:off+lineEnd], "\r")
		if line == "---" || line == "..." {
			return rest[off+lineEnd+1:]
		}
		off += lineEnd + 1
	}
	return markdown
}
