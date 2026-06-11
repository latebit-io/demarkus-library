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
