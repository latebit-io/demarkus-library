package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

func TestTrailRendersModesAndFocusedLive(t *testing.T) {
	svc := &fakeReading{
		docs: map[string]domain.Document{
			"/a.md": {Title: "Pane A", Path: "/a.md", HTML: "<p>a</p>", Status: "accepted"},
			"/b.md": {Title: "Pane B", Path: "/b.md", HTML: "<p>b</p>", Status: "draft"},
			"/c.md": {Title: "Pane C", Path: "/c.md", HTML: "<p>c</p>", Status: "wip",
				Tags: []string{"adr"}, Version: "3"},
		},
	}
	rec := get(readingApp(t, svc), "/t/w.io/d/a.md/~/w.io/d/b.md/~/w.io/d/c.md")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()

	// Focused-live policy: only the focused pane reads live.
	want := []string{"ReadCached /a.md", "ReadCached /b.md", "Read /c.md"}
	if strings.Join(svc.calls, ",") != strings.Join(want, ",") {
		t.Errorf("calls = %v, want %v", svc.calls, want)
	}

	// Display modes by distance from focus (decision 3).
	for _, wantFrag := range []string{
		`class="pane spine"`,             // pane A
		`class="pane body"`,              // pane B — immediate parent
		`class="pane focused"`,           // pane C
		`class="status status-accepted"`, // spine carries its badge
	} {
		if !strings.Contains(body, wantFrag) {
			t.Errorf("canvas missing %q", wantFrag)
		}
	}
	// Only the focused pane renders the margin.
	if got := strings.Count(body, `class="doc-meta"`); got != 1 {
		t.Errorf("doc-meta count = %d, want 1 (focused only)", got)
	}
	// The parent renders its body, the spine does not.
	if !strings.Contains(body, "<p>b</p>") || strings.Contains(body, "<p>a</p>") {
		t.Errorf("pane body rendering wrong: parent body and no spine body expected")
	}
	if !strings.Contains(body, `class="canvas"`) {
		t.Errorf("canvas main missing")
	}
}

func TestTrailLinksCarryPostClickState(t *testing.T) {
	svc := &fakeReading{
		docs: map[string]domain.Document{
			"/a.md": {Title: "A", Path: "/a.md", HTML: `<p><a href="/b.md">to b</a> <a href="/z.md">to z</a></p>`},
			"/b.md": {Title: "B", Path: "/b.md", HTML: "<p>b</p>"},
		},
	}
	body := get(readingApp(t, svc), "/t/w.io/d/a.md/~/w.io/d/b.md?focus=0").Body.String()

	// The link to the pane already open to the right: same path, focus
	// jumps (dedup) — and it is the highlighted breadcrumb. (previewize adds
	// hover attrs between href and class, so assert both, not their adjacency.)
	if !strings.Contains(body, `href="/t/w.io/d/a.md/~/w.io/d/b.md"`) ||
		!strings.Contains(body, `class="active-link"`) {
		t.Errorf("active link to open pane missing: %s", body)
	}
	// A link to a new doc from pane 0 truncates pane 1 away and appends.
	if !strings.Contains(body, `href="/t/w.io/d/a.md/~/w.io/d/z.md"`) {
		t.Errorf("post-click trail URL missing for new target")
	}
}

func TestTrailUnfocusedErrorBecomesTombstone(t *testing.T) {
	svc := &fakeReading{
		doc:  domain.Document{Title: "OK", Path: "/b.md", HTML: "<p>b</p>"},
		errs: map[string]error{"/gone.md": domain.ErrNotFound},
	}
	rec := get(readingApp(t, svc), "/t/w.io/d/gone.md/~/w.io/d/b.md")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a stale waypoint must not kill the trail", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `class="pane spine gone"`) {
		t.Errorf("tombstone spine missing")
	}
}

func TestTrailFocusedErrorIsAnError(t *testing.T) {
	svc := &fakeReading{errs: map[string]error{"/gone.md": domain.ErrNotFound}}
	if rec := get(readingApp(t, svc), "/t/w.io/d/gone.md"); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for a missing focused pane", rec.Code)
	}
}

func TestTrailMalformedIs400(t *testing.T) {
	svc := &fakeReading{}
	if rec := get(readingApp(t, svc), "/t/w.io/versions/x.md"); rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestTrailPaneKindsRouteToVerbs(t *testing.T) {
	svc := &fakeReading{doc: domain.Document{Title: "x", HTML: "<p>x</p>"}}
	get(readingApp(t, svc), "/t/w.io/d/plans//~/w.io/tags/adr/~/w.io/d/x.md?focus=1")
	want := []string{"BrowseCached /plans/", "Tag adr", "ReadCached /x.md"}
	if strings.Join(svc.calls, ",") != strings.Join(want, ",") {
		t.Errorf("calls = %v, want %v", svc.calls, want)
	}
}
