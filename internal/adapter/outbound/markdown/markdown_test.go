package markdown

import (
	"strings"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// render is the test shorthand for the common case: the sanitized HTML of a
// successful render.
func render(t *testing.T, src string) string {
	t.Helper()
	out, err := NewRenderer().Render(src)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	return out.HTML
}

func TestRenderProducesHTML(t *testing.T) {
	// An H2 (not the leading H1, which is lifted out as the title) so this
	// exercises heading + bold rendering.
	html := render(t, "## Section\n\nsome **bold** text")
	if !strings.Contains(html, "<h2") || !strings.Contains(html, "<strong>") {
		t.Errorf("expected rendered heading + bold, got %q", html)
	}
}

func TestRenderLiftsLeadingH1AsTitle(t *testing.T) {
	// The reading-room pane renders the title itself, so a body that opens with
	// an H1 must surface it as Title and drop it from HTML (no double header).
	out, err := NewRenderer().Render("# Patterns\n\nbody text")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if out.Title != "Patterns" {
		t.Errorf("Title = %q, want Patterns", out.Title)
	}
	if strings.Contains(out.HTML, "<h1") {
		t.Errorf("leading H1 must be stripped from HTML: %q", out.HTML)
	}
	if !strings.Contains(out.HTML, "body text") {
		t.Errorf("body content lost: %q", out.HTML)
	}
	// An H1 that is not the first block is real content — left untouched.
	out2, err := NewRenderer().Render("intro paragraph\n\n# Later")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if out2.Title != "" || !strings.Contains(out2.HTML, "<h1") {
		t.Errorf("non-leading H1 must stay: title=%q html=%q", out2.Title, out2.HTML)
	}
}

func TestRenderSanitizesScript(t *testing.T) {
	html := render(t, "hi <script>alert(1)</script> there")
	if strings.Contains(html, "<script>") {
		t.Errorf("script tag not sanitized: %q", html)
	}
}

func TestSplitFrontmatter(t *testing.T) {
	cases := []struct {
		name, in, wantFence, wantBody string
	}{
		{"obsidian roundtrip frontmatter",
			"---\nversion: 4\narchived: false\nprevious-hash: sha256-abc\nmeta.agent: claude-code\n---\n# Engineering Values\n\nBody.",
			"version: 4\narchived: false\nprevious-hash: sha256-abc\nmeta.agent: claude-code\n",
			"# Engineering Values\n\nBody."},
		{"closing dots fence", "---\ntitle: x\n...\n# Doc", "title: x\n", "# Doc"},
		{"crlf fences", "---\r\ntitle: x\r\n---\r\n# Doc", "title: x\r\n", "# Doc"},
		{"no frontmatter untouched", "# Doc\n\n---\n\nmore", "", "# Doc\n\n---\n\nmore"},
		{"thematic break around content untouched", "---\nintro\n---\nbody", "", "---\nintro\n---\nbody"},
		{"non-mapping fence untouched", "---\n- a\n- b\n---\nbody", "", "---\n- a\n- b\n---\nbody"},
		{"empty fence stripped", "---\n---\nbody", "", "body"},
		{"nested-only mapping stripped", "---\nnested:\n  k: v\n---\nbody", "nested:\n  k: v\n", "body"},
		{"unclosed fence untouched", "---\ntitle: x\nno closer", "", "---\ntitle: x\nno closer"},
		{"content before fence untouched", "intro\n---\nx\n---\n", "", "intro\n---\nx\n---\n"},
		{"closer at EOF no newline", "---\ntitle: x\n---", "title: x\n", ""},
		{"closer at EOF crlf no newline", "---\r\ntitle: x\r\n---", "title: x\r\n", ""},
		{"empty body", "", "", ""},
		{"bare fence only", "---\n", "", "---\n"},
	}
	for _, tc := range cases {
		fence, body := splitFrontmatter(tc.in)
		if fence != tc.wantFence || body != tc.wantBody {
			t.Errorf("%s: splitFrontmatter(%q) = (%q, %q), want (%q, %q)",
				tc.name, tc.in, fence, body, tc.wantFence, tc.wantBody)
		}
	}
}

func TestParseFrontmatter(t *testing.T) {
	props := parseFrontmatter("status: accepted\ntags:\n  - a\n  - b\nnested:\n  k: v\nempty: ''\nversion: 4\n")
	want := []domain.Property{
		{Key: "status", Value: "accepted"},
		{Key: "tags", Value: "a, b"},
		// nested mapping and empty value skipped
		{Key: "version", Value: "4"},
	}
	if len(props) != len(want) {
		t.Fatalf("props = %v, want %v", props, want)
	}
	for i := range want {
		if props[i] != want[i] {
			t.Errorf("props[%d] = %v, want %v", i, props[i], want[i])
		}
	}
}

func TestParseFrontmatterGarbage(t *testing.T) {
	for _, fence := range []string{"", "   ", "just text, not a mapping", "- a\n- b\n", ": ["} {
		if props := parseFrontmatter(fence); props != nil {
			t.Errorf("parseFrontmatter(%q) = %v, want nil", fence, props)
		}
	}
}

func TestRenderParsesFrontmatterIntoProperties(t *testing.T) {
	out, err := NewRenderer().Render("---\nversion: 4\narchived: false\n---\n# Values\n\nReal content.")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(out.HTML, "version: 4") || strings.Contains(out.HTML, "archived") {
		t.Errorf("frontmatter leaked into HTML: %s", out.HTML)
	}
	// The leading H1 after the fence is lifted out as the title; the body
	// content survives.
	if out.Title != "Values" || !strings.Contains(out.HTML, "Real content.") {
		t.Errorf("content/title wrong: title=%q html=%s", out.Title, out.HTML)
	}
	if len(out.Properties) != 2 || out.Properties[0] != (domain.Property{Key: "version", Value: "4"}) ||
		out.Properties[1] != (domain.Property{Key: "archived", Value: "false"}) {
		t.Errorf("properties = %v", out.Properties)
	}
}

func TestRenderSidenote(t *testing.T) {
	html := render(t, "A claim.[^1] More text.\n\n[^1]: The *aside* that backs it.")
	if !strings.Contains(html, `<sup class="sidenote-number"></sup><span class="sidenote">`) {
		t.Errorf("sidenote span missing: %q", html)
	}
	if !strings.Contains(html, "<em>aside</em>") {
		t.Errorf("inline markup inside sidenote lost: %q", html)
	}
	if strings.Contains(html, "footnote") {
		t.Errorf("qualifying footnote should fully convert (no foot list left): %q", html)
	}
}

func TestRenderSidenoteFallbacks(t *testing.T) {
	cases := []struct{ name, in string }{
		{"multi-paragraph note", "Claim.[^1]\n\n[^1]: First para.\n\n    Second para."},
		{"multi-ref note", "One.[^1] Two.[^1]\n\n[^1]: Shared note."},
	}
	for _, tc := range cases {
		html := render(t, tc.in)
		if strings.Contains(html, `class="sidenote"`) {
			t.Errorf("%s: must stay a footnote, got sidenote: %q", tc.name, html)
		}
		// The classic footnote apparatus must survive the sanitizer so the
		// fallback actually works: ref link out, listed note, backref home.
		for _, want := range []string{`class="footnote-ref"`, `class="footnotes"`, `href="#fn:1"`, `id="fn:1"`} {
			if !strings.Contains(html, want) {
				t.Errorf("%s: footnote fallback missing %q: %q", tc.name, want, html)
			}
		}
	}
}

func TestRenderSidenoteMixedWithFootnote(t *testing.T) {
	// One qualifying note and one multi-ref note in the same document: the
	// first becomes a sidenote, the second stays in a foot list.
	html := render(t, "A.[^1] B.[^2] C.[^2]\n\n[^1]: Margin note.\n\n[^2]: Shared.")
	if !strings.Contains(html, `class="sidenote"`) {
		t.Errorf("qualifying note should convert: %q", html)
	}
	if !strings.Contains(html, `class="footnotes"`) || !strings.Contains(html, "Shared.") {
		t.Errorf("shared note should remain a footnote: %q", html)
	}
	if strings.Contains(html, "Margin note.</p>") {
		t.Errorf("converted note should leave the foot list: %q", html)
	}
}

func TestRenderHighlightsFencedCode(t *testing.T) {
	html := render(t, "```go\nfunc main() {}\n```")
	// chroma classes-mode markers must survive sanitization: the <pre>
	// carries class="chroma" and tokens are classed spans (`kd` is the
	// keyword-declaration class `func` lands in).
	if !strings.Contains(html, `class="chroma"`) {
		t.Errorf("chroma pre class missing: %q", html)
	}
	if !strings.Contains(html, `<span class="kd">func</span>`) {
		t.Errorf("classed token spans missing: %q", html)
	}
	if strings.Contains(html, "style=") {
		t.Errorf("inline styles must not appear (classes mode + sanitizer): %q", html)
	}
}

func TestRenderUnknownLanguagePlain(t *testing.T) {
	html := render(t, "```nosuchlang-xyz\nplain text\n```")
	if !strings.Contains(html, "plain text") {
		t.Errorf("code body lost: %q", html)
	}
}

func TestRenderStripsNonChromaClasses(t *testing.T) {
	// Short tokens (nav, btn, foo1) shape-matched the old `[a-z]{1,3}[0-9]?`
	// pattern but are not chroma class names — the allowlist is built from
	// chroma.StandardTypes, so they must be stripped, including when mixed
	// with a legitimate atom (`bg nav`: the whole attribute value must match).
	for _, class := range []string{"navbar evil-site-class", "nav", "btn", "foo1", "bg nav"} {
		html := render(t, `hi <span class="`+class+`">x</span>`)
		if strings.Contains(html, "class=") {
			t.Errorf("non-chroma class %q survived sanitization: %q", class, html)
		}
	}
}

func TestRenderStripsAuthoredSidenoteIDs(t *testing.T) {
	// The sidenote/footnote allowances must not open DOM clobbering: only
	// fn-shaped ids pass, and only on the elements goldmark emits them on.
	html := render(t, `x <sup id="login">y</sup> <a id="fn:1" href="#fn:1">z</a>`)
	if strings.Contains(html, `id="login"`) {
		t.Errorf("author-controlled id survived: %q", html)
	}
	if strings.Contains(html, `<a id=`) {
		t.Errorf("id allowed on <a> (only sup/li): %q", html)
	}
}

func TestRenderGFMAlerts(t *testing.T) {
	html := render(t, "> [!WARNING]\n> Careful here.")
	for _, want := range []string{
		`class="callout callout-warning"`,
		`class="callout-title"`,
		`<p class="callout-title-text">Warning</p>`,
		`class="callout-body"`,
		"Careful here.",
		"⚠️",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %q in %q", want, html)
		}
	}
	if strings.Contains(html, "data-callout") {
		t.Errorf("data-callout should be stripped by the sanitizer: %q", html)
	}
	if strings.Contains(html, "<svg") {
		t.Errorf("no svg expected with the emoji icon set: %q", html)
	}
}

func TestRenderEmojiShortcode(t *testing.T) {
	html := render(t, "ship it :rocket:")
	if !strings.Contains(html, "\U0001F680") && !strings.Contains(html, "&#x1f680;") {
		t.Errorf("rocket emoji missing: %q", html)
	}
}

func TestRenderPlainBlockquoteUntouched(t *testing.T) {
	html := render(t, "> just a quote")
	if !strings.Contains(html, "<blockquote>") {
		t.Errorf("plain blockquote should stay a blockquote: %q", html)
	}
	if strings.Contains(html, "callout") {
		t.Errorf("plain blockquote must not become an alert: %q", html)
	}
}

func TestRenderMermaidBlockKeepsSourceAndClass(t *testing.T) {
	html := render(t, "```mermaid\ngraph TD; A-->B;\n```")
	// The island (islands.js) keys on language-mermaid; the source must
	// stay a readable, unhighlighted code block (the no-JS degradation).
	if !strings.Contains(html, `<code class="language-mermaid">`) {
		t.Errorf("language-mermaid class missing: %q", html)
	}
	if !strings.Contains(html, "graph TD; A--&gt;B;") {
		t.Errorf("mermaid source mangled: %q", html)
	}
	if strings.Contains(html, "chroma") {
		t.Errorf("mermaid block must not be syntax-highlighted: %q", html)
	}
}

func TestRenderMathPassthrough(t *testing.T) {
	cases := []struct{ name, in, want string }{
		// Underscores inside math must not become <em> — the passthrough
		// extension shields TeX from markdown processing.
		{"inline", `inline \(a_1 + b_2\) math`, `\(a_1 + b_2\)`},
		{"display dollars", "$$\nE = mc^2\n$$", "E = mc^2"},
		{"display brackets", `\[x^2\]`, `\[x^2\]`},
	}
	for _, c := range cases {
		html := render(t, c.in)
		if !strings.Contains(html, c.want) {
			t.Errorf("%s: want %q in %q", c.name, c.want, html)
		}
		if strings.Contains(html, "<em>") {
			t.Errorf("%s: math fell through to markdown emphasis: %q", c.name, html)
		}
	}
}

func TestRenderMathSanitized(t *testing.T) {
	html := render(t, `evil \(<script>alert(1)</script>\)`)
	if strings.Contains(html, "<script>") {
		t.Errorf("passthrough content must still be sanitized: %q", html)
	}
}

func TestRenderDollarAmountsUntouched(t *testing.T) {
	html := render(t, "price is $5 and $10 total")
	if !strings.Contains(html, "$5 and $10") {
		t.Errorf("dollar amounts must not be eaten as math: %q", html)
	}
}
