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
		`class="pane spine"`,   // pane A
		`class="pane body"`,    // pane B — immediate parent
		`class="pane focused"`, // pane C
	} {
		if !strings.Contains(body, wantFrag) {
			t.Errorf("canvas missing %q", wantFrag)
		}
	}
	// Spine demotion (ADR 0006 §2): a collapsed pane is a title-only re-expand
	// rail — no status badge. Pane A is accepted and collapsed, so its badge is
	// gone; the focused pane (C, wip) still shows its margin badge.
	if strings.Contains(body, `class="status status-accepted"`) {
		t.Errorf("spine must not carry a status badge (ADR 0006 §2)")
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

func TestTrailReaderOverlayRenders(t *testing.T) {
	svc := &fakeReading{docs: map[string]domain.Document{
		"/a.md": {Title: "Pane A", Path: "/a.md", HTML: "<p>a</p>", Status: "accepted", Version: "2"},
	}}
	body := get(readingApp(t, svc), "/t/w.io/d/a.md?reader=0").Body.String()
	for _, want := range []string{
		`class="reader-backdrop"`,
		`class="reader-scrim"`,
		`class="reader-panel"`,
		`class="reader-close"`,
		`href="/t/w.io/d/a.md"`, // ✕ + scrim close to the bare trail
	} {
		if !strings.Contains(body, want) {
			t.Errorf("overlay missing %q", want)
		}
	}
	// The canvas still renders behind the overlay — context preserved.
	if !strings.Contains(body, `class="canvas"`) {
		t.Error("canvas should still render behind the reader overlay")
	}
	// The trust margin renders in the overlay AND on the focused canvas pane.
	if got := strings.Count(body, `class="doc-meta"`); got != 2 {
		t.Errorf("doc-meta count = %d, want 2 (focused pane + overlay)", got)
	}
}

func TestTrailReaderIsALensNotANavigation(t *testing.T) {
	// Regression: opening reader on a NON-focused pane must not relayout the
	// canvas behind — focus stays put, the overlay just shows that pane's doc.
	svc := &fakeReading{docs: map[string]domain.Document{
		"/a.md": {Title: "Pane A", Path: "/a.md", HTML: "<p>a</p>"},
		"/b.md": {Title: "Pane B", Path: "/b.md", HTML: "<p>b</p>"},
	}}
	// Two-pane trail; focus defaults to the last pane (B). Open reader on A.
	body := get(readingApp(t, svc), "/t/w.io/d/a.md/~/w.io/d/b.md?reader=0").Body.String()

	// The canvas behind is unchanged: B stays the focused (wide) pane, A stays
	// the body parent — A does NOT become focused and B does NOT collapse.
	if !strings.Contains(body, `class="pane body"`) || !strings.Contains(body, `class="pane focused"`) {
		t.Error("opening reader on pane A collapsed the canvas behind it")
	}
	if !strings.Contains(body, `class="reader-backdrop"`) {
		t.Error("overlay missing")
	}
	// Focused-live is preserved: only B (focus) reads live; A (reader) comes
	// from cache — the overlay adds no second world read.
	want := []string{"ReadCached /a.md", "Read /b.md"}
	if strings.Join(svc.calls, ",") != strings.Join(want, ",") {
		t.Errorf("calls = %v, want %v (reader pane stays cached; only focus reads live)", svc.calls, want)
	}
	// Close keeps the original focus (B = last ⇒ bare trail).
	if !strings.Contains(body, `<a class="reader-scrim" href="/t/w.io/d/a.md/~/w.io/d/b.md"`) {
		t.Error("close URL must preserve focus, not reset it")
	}
}

func TestTrailNoReaderNoOverlay(t *testing.T) {
	svc := &fakeReading{docs: map[string]domain.Document{
		"/a.md": {Title: "A", Path: "/a.md", HTML: "<p>a</p>"},
	}}
	body := get(readingApp(t, svc), "/t/w.io/d/a.md").Body.String()
	// (assert the rendered overlay element, not the always-present CSS rule)
	if strings.Contains(body, `class="reader-backdrop"`) {
		t.Error("no ?reader ⇒ no overlay (no-regression)")
	}
	// The "reader" affordance is still offered, to open it.
	if !strings.Contains(body, `href="/t/w.io/d/a.md?reader=0"`) {
		t.Error("reader affordance link missing")
	}
}

func TestTrailReaderOutOfRangeIgnored(t *testing.T) {
	svc := &fakeReading{docs: map[string]domain.Document{
		"/a.md": {Title: "A", Path: "/a.md", HTML: "<p>a</p>"},
	}}
	body := get(readingApp(t, svc), "/t/w.io/d/a.md?reader=9").Body.String()
	if strings.Contains(body, `class="reader-backdrop"`) {
		t.Error("out-of-range ?reader must be ignored, not overlaid")
	}
}

func TestTrailReaderPersistsOnNavigate(t *testing.T) {
	svc := &fakeReading{docs: map[string]domain.Document{
		"/a.md": {Title: "A", Path: "/a.md", HTML: `<p><a href="/b.md">to b</a></p>`},
	}}
	body := get(readingApp(t, svc), "/t/w.io/d/a.md?reader=0").Body.String()
	// Inside the overlay, a link continues the trail AND stays in reader on
	// the newly focused pane (persist-on-navigate, the feature's point).
	if !strings.Contains(body, `href="/t/w.io/d/a.md/~/w.io/d/b.md?reader=1"`) {
		t.Errorf("reader-persisting link missing: %s", body)
	}
	// The canvas copy of the same pane keeps a plain (overlay-free) link — the
	// trailing quote disambiguates it from the ?reader= variant above.
	if !strings.Contains(body, `href="/t/w.io/d/a.md/~/w.io/d/b.md"`) {
		t.Error("canvas link should not carry the overlay")
	}
}

func TestTrailReaderMarginIsNotADeadEnd(t *testing.T) {
	svc := &fakeReading{docs: map[string]domain.Document{
		"/a.md": {Title: "A", Path: "/a.md", HTML: "<p>a</p>"},
	}}
	body := get(authedApp(t, svc), "/t/w.io/d/a.md?reader=0").Body.String()
	// Reading mode is not a dead-end: the auth-gated write affordances still
	// render in the reader margin so you can branch and act.
	if !strings.Contains(body, "/w/w.io/edit/a.md") || !strings.Contains(body, "/w/w.io/append/a.md") {
		t.Error("auth-gated affordances should still render in the reader margin")
	}
	// The overlay offers no redundant "reader" link to itself: the two that
	// remain are the canvas pane's head + margin affordances.
	if got := strings.Count(body, `>reader</a>`); got != 2 {
		t.Errorf("reader affordance count = %d, want 2 (canvas only; overlay offers none)", got)
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
