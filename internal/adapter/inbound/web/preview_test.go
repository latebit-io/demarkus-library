package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

func TestPreviewizeWrapsDocumentLinksOnly(t *testing.T) {
	// Input is post-rewriteLinks: hrefs are already /w/ routes.
	in := `<p>` +
		`<a href="/w/soul/d/a.md">doc</a> ` +
		`<a href="/w/soul/d/plans/">listing</a> ` +
		`<a href="https://x.com">ext</a>` +
		`</p>`
	out := previewize(in)

	// The document link gains the hover card scaffolding and htmx attrs.
	if !strings.Contains(out, `class="preview-host"`) || !strings.Contains(out, `class="preview-card"`) {
		t.Errorf("document link not wrapped: %s", out)
	}
	if !strings.Contains(out, `hx-get="/w/soul/preview/a.md"`) ||
		!strings.Contains(out, `hx-trigger="mouseenter delay:300ms once"`) {
		t.Errorf("hover attrs missing: %s", out)
	}
	// Exactly one host wrapper — the listing and external link are not wrapped.
	if n := strings.Count(out, `class="preview-host"`); n != 1 {
		t.Errorf("preview-host count = %d, want 1 (doc only): %s", n, out)
	}
	if strings.Contains(out, `hx-get="/w/soul/preview/plans/"`) {
		t.Errorf("listing should not be previewable: %s", out)
	}
}

func TestPreviewSnippetFirstParagraph(t *testing.T) {
	html := `<h1>Title</h1><p>First paragraph of prose.</p><p>Second.</p>`
	if got := previewSnippet(html); got != "First paragraph of prose." {
		t.Errorf("snippet = %q", got)
	}
	if got := previewSnippet(`<h1>only heading</h1>`); got != "" {
		t.Errorf("no-paragraph snippet = %q, want empty", got)
	}
}

func TestPreviewSnippetTruncates(t *testing.T) {
	long := "<p>" + strings.Repeat("word ", 100) + "</p>"
	got := previewSnippet(long)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("long snippet not truncated: %q", got)
	}
	if len([]rune(got)) > previewSnippetLen {
		t.Errorf("snippet too long: %d runes", len([]rune(got)))
	}
}

func TestPreviewHandlerServesCard(t *testing.T) {
	svc := &fakeReading{doc: domain.Document{
		Title: "Architecture", Status: "accepted", Path: "/architecture.md",
		HTML: "<p>How the hexagon is wired.</p>",
	}}
	rec := get(readingApp(t, svc), "/w/soul/preview/architecture.md")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Architecture", "How the hexagon is wired.",
		"status-accepted", "mark://soul/architecture.md", `href="/w/soul/d/architecture.md"`} {
		if !strings.Contains(body, want) {
			t.Errorf("card missing %q: %s", want, body)
		}
	}
	// Served from the cache — a hover must not spend the live read budget.
	if svc.called != "ReadCached" {
		t.Errorf("preview used %q, want ReadCached", svc.called)
	}
}

func TestPreviewHandlerMissingIsNoContent(t *testing.T) {
	svc := &fakeReading{err: domain.ErrNotFound}
	rec := get(readingApp(t, svc), "/w/soul/preview/gone.md")
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204 (empty card stays hidden)", rec.Code)
	}
}
