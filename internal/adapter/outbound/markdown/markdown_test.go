package markdown

import (
	"strings"
	"testing"
)

func TestRenderProducesHTML(t *testing.T) {
	html, err := NewRenderer().Render("# Title\n\nsome **bold** text")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(html, "<h1") || !strings.Contains(html, "<strong>") {
		t.Errorf("expected rendered heading + bold, got %q", html)
	}
}

func TestRenderSanitizesScript(t *testing.T) {
	html, err := NewRenderer().Render("hi <script>alert(1)</script> there")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(html, "<script>") {
		t.Errorf("script tag not sanitized: %q", html)
	}
}

func TestStripFrontmatter(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"obsidian roundtrip frontmatter",
			"---\nversion: 4\narchived: false\nprevious-hash: sha256-abc\nmeta.agent: claude-code\n---\n# Engineering Values\n\nBody.",
			"# Engineering Values\n\nBody."},
		{"closing dots fence", "---\ntitle: x\n...\n# Doc", "# Doc"},
		{"crlf fences", "---\r\ntitle: x\r\n---\r\n# Doc", "# Doc"},
		{"no frontmatter untouched", "# Doc\n\n---\n\nmore", "# Doc\n\n---\n\nmore"},
		{"unclosed fence untouched", "---\ntitle: x\nno closer", "---\ntitle: x\nno closer"},
		{"content before fence untouched", "intro\n---\nx\n---\n", "intro\n---\nx\n---\n"},
		{"closer at EOF no newline", "---\ntitle: x\n---", ""},
		{"closer at EOF crlf no newline", "---\r\ntitle: x\r\n---", ""},
		{"empty body", "", ""},
		{"bare fence only", "---\n", "---\n"},
	}
	for _, tc := range cases {
		if got := stripFrontmatter(tc.in); got != tc.want {
			t.Errorf("%s: stripFrontmatter(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestRenderStripsFrontmatter(t *testing.T) {
	r := NewRenderer()
	html, err := r.Render("---\nversion: 4\narchived: false\n---\n# Values\n\nReal content.")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(html, "version: 4") || strings.Contains(html, "archived") {
		t.Errorf("frontmatter leaked into HTML: %s", html)
	}
	if !strings.Contains(html, "<h1") || !strings.Contains(html, "Real content.") {
		t.Errorf("content lost: %s", html)
	}
}

func TestRenderHighlightsFencedCode(t *testing.T) {
	html, err := NewRenderer().Render("```go\nfunc main() {}\n```")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
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
	html, err := NewRenderer().Render("```nosuchlang-xyz\nplain text\n```")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(html, "plain text") {
		t.Errorf("code body lost: %q", html)
	}
}

func TestRenderStripsNonChromaClasses(t *testing.T) {
	html, err := NewRenderer().Render(`hi <span class="navbar evil-site-class">x</span>`)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(html, "navbar") {
		t.Errorf("non-chroma class survived sanitization: %q", html)
	}
}

func TestRenderGFMAlerts(t *testing.T) {
	html, err := NewRenderer().Render("> [!WARNING]\n> Careful here.")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
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
	html, err := NewRenderer().Render("ship it :rocket:")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(html, "\U0001F680") && !strings.Contains(html, "&#x1f680;") {
		t.Errorf("rocket emoji missing: %q", html)
	}
}

func TestRenderPlainBlockquoteUntouched(t *testing.T) {
	html, err := NewRenderer().Render("> just a quote")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(html, "<blockquote>") {
		t.Errorf("plain blockquote should stay a blockquote: %q", html)
	}
	if strings.Contains(html, "callout") {
		t.Errorf("plain blockquote must not become an alert: %q", html)
	}
}
