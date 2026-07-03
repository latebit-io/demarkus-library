package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

func TestTrailCodec_LibrarianPane(t *testing.T) {
	t.Parallel()

	// Bare `a` parses, round-trips, and joins mixed trails.
	tr, err := parseTrail("a", "", "")
	if err != nil {
		t.Fatalf("parseTrail(a): %v", err)
	}
	if tr.Panes[0].Kind != paneLibrarian || trailURL(tr) != "/t/a" {
		t.Errorf("round-trip = %q (kind %q); want /t/a", trailURL(tr), tr.Panes[0].Kind)
	}

	tr, err = parseTrail("u/~/w.io/d/x.md/~/a", "", "")
	if err != nil {
		t.Fatalf("parseTrail(mixed): %v", err)
	}
	if got := trailURL(tr); got != "/t/u/~/w.io/d/x.md/~/a" {
		t.Errorf("mixed round-trip = %q", got)
	}

	// The click algebra dedups: clicking toward an existing librarian pane
	// focuses it instead of appending a second one.
	after := trailAfterClick(tr, 2, paneAddr{Kind: paneLibrarian})
	if len(after.Panes) != 3 || after.Focus != 2 {
		t.Errorf("dedup failed: %d panes focus %d", len(after.Panes), after.Focus)
	}

	// A world-scoped `a` chunk is not a thing — reject it.
	if _, err := parseTrail("w.io/a/x", "", ""); err == nil {
		t.Error("world-scoped librarian chunk accepted; want error")
	}
	// The reader overlay is prose-only; ?reader= on a librarian pane is ignored.
	tr, err = parseTrail("a", "", "0")
	if err != nil || tr.Reader != -1 {
		t.Errorf("reader overlay on librarian pane: Reader = %d; want -1", tr.Reader)
	}
}

func TestLibrarianPane_TranscriptAndForm(t *testing.T) {
	t.Parallel()

	lib := &fakeLibrarian{history: []domain.LibrarianExchange{
		{Question: "where is the runbook?", Answer: "See [it](mark://w.io/ops/run.md)."},
	}}
	e := librarianApp(t, lib)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/t/w.io/d/x.md/~/a", http.NoBody))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"where is the runbook?",                // the question renders
		`action="/a/ask"`,                      // real form (no-JS)
		`hx-post="/a/ask"`,                     // htmx enhancement
		`name="_csrf"`,                         // CSRF'd (plan step 3)
		`name="trail" value="w.io/d/x.md/~/a"`, // trail context rides along
		`name="idx" value="1"`,
		"librarian-transcript",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("pane missing %q\nbody:\n%.2000s", want, body)
		}
	}
	// The answer went through the render pipeline (fake Preview wraps <p>),
	// not raw markdown into the page.
	if !strings.Contains(body, "<p>See [it](mark://w.io/ops/run.md).</p>") {
		t.Errorf("answer not rendered through Preview:\n%.2000s", body)
	}
}

func TestRenderAnswer_CitationsContinueTheTrail(t *testing.T) {
	t.Parallel()

	// The fake Preview passes markdown through verbatim inside <p>, so feed
	// it an already-anchored citation: rewriteLinks must resolve the mark://
	// href to an in-app route and trailizeLinks must rewrite that to the
	// post-click trail URL from the librarian pane.
	svc := &fakeReading{}
	h := NewReadingHandler(svc, "soul.demarkus.io", "/index.md")
	tr := trail{Panes: []paneAddr{{Kind: paneLibrarian}}, Focus: 0, Reader: -1}

	got := string(h.renderAnswer(`<a href="mark://w.io/ops/run.md">the runbook</a>`, tr, 0))
	if !strings.Contains(got, `href="/t/a/~/w.io/d/ops/run.md"`) {
		t.Errorf("citation not trailized:\n%s", got)
	}
}

func TestLibrarianPane_UnfocusedHasNoForm(t *testing.T) {
	t.Parallel()

	lib := &fakeLibrarian{}
	e := librarianApp(t, lib)
	rec := httptest.NewRecorder()
	// Librarian is pane 0, focus on pane 1 → librarian renders body-only.
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/t/a/~/w.io/d/x.md?focus=1", http.NoBody))

	if body := rec.Body.String(); strings.Contains(body, `action="/a/ask"`) {
		t.Errorf("unfocused librarian pane renders the ask form:\n%.2000s", body)
	}
}

func TestLibrarianPane_FeatureDark(t *testing.T) {
	t.Parallel()

	e := spikeApp(t) // no librarian wired
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/t/a", http.NoBody))

	body := rec.Body.String()
	if !strings.Contains(body, "not on duty") {
		t.Errorf("feature-dark pane missing the not-on-duty notice:\n%.1500s", body)
	}
	if strings.Contains(body, `action="/a/ask"`) {
		t.Errorf("feature-dark pane renders the ask form")
	}
}

func TestAskLibrarian_HtmxReturnsLiveExchange(t *testing.T) {
	t.Parallel()

	lib := &fakeLibrarian{}
	e := librarianApp(t, lib)

	form := url.Values{"question": {"what is the floor?"}, "trail": {"a"}, "idx": {"0"}}
	req := httptest.NewRequest(http.MethodPost, "/a/ask", strings.NewReader(form.Encode()))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"what is the floor?",
		// html/template's URL escaper renders + as &#43; in attributes.
		`sse-connect="/a/stream?i=0&amp;q=what&#43;is&#43;the&#43;floor%3F&amp;t=a"`,
		`sse-close="done"`,
		`sse-swap="rendered"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("fragment missing %q\nbody:\n%s", want, body)
		}
	}
	// The htmx path must NOT run the ask — the SSE GET starts the run.
	if len(lib.asked) != 0 {
		t.Errorf("htmx ask ran the question synchronously: %v", lib.asked)
	}
}

func TestAskLibrarian_NoJSRunsAndRedirects(t *testing.T) {
	t.Parallel()

	lib := &fakeLibrarian{events: []domain.LibrarianEvent{
		{Kind: domain.LibrarianAnswer, Text: "answered"},
		{Kind: domain.LibrarianDone},
	}}
	e := librarianApp(t, lib)

	form := url.Values{"question": {"plain form ask"}, "trail": {"a"}, "idx": {"0"}}
	req := httptest.NewRequest(http.MethodPost, "/a/ask", strings.NewReader(form.Encode()))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d; want 303 (body: %s)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/t/a" {
		t.Errorf("Location = %q; want /t/a", loc)
	}
	if len(lib.asked) != 1 || lib.asked[0] != "plain form ask" {
		t.Errorf("no-JS ask did not run synchronously: %v", lib.asked)
	}
}

func TestAskLibrarian_BusySetsNotice(t *testing.T) {
	t.Parallel()

	lib := &fakeLibrarian{askErr: domain.ErrLibrarianBusy}
	e := librarianApp(t, lib)

	form := url.Values{"question": {"impatient"}, "trail": {"a"}, "idx": {"0"}}
	req := httptest.NewRequest(http.MethodPost, "/a/ask", strings.NewReader(form.Encode()))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d; want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "notice=") {
		t.Errorf("Location = %q; want a notice param", loc)
	}
}

func TestAskLibrarian_ValidatesQuestionAndAvailability(t *testing.T) {
	t.Parallel()

	// Feature-dark: asking 404s.
	req := httptest.NewRequest(http.MethodPost, "/a/ask",
		strings.NewReader(url.Values{"question": {"anyone there?"}}.Encode()))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	rec := httptest.NewRecorder()
	spikeApp(t).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("feature-dark ask status = %d; want 404", rec.Code)
	}

	// Blank question: 400.
	e := librarianApp(t, &fakeLibrarian{})
	req = httptest.NewRequest(http.MethodPost, "/a/ask",
		strings.NewReader(url.Values{"question": {"   "}}.Encode()))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("blank question status = %d; want 400", rec.Code)
	}
}

func TestLibrarianEntrance_RedirectsToTrail(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	spikeApp(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/a", http.NoBody))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/t/a" {
		t.Errorf("entrance = %d %q; want 303 /t/a", rec.Code, rec.Header().Get("Location"))
	}
}
