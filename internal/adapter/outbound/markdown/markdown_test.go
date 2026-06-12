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
