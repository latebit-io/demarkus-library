package web

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// spikeApp wires the reading routes with no librarian — the /a/ surface in
// its feature-dark posture (pane says not-on-duty; only the soak streams).
func spikeApp(t *testing.T) *echo.Echo {
	t.Helper()
	return readingApp(t, &fakeReading{doc: domain.Document{Title: "X", Path: "/x.md", HTML: "<p>x</p>"}})
}

// librarianApp wires the reading routes with a scripted librarian.
func librarianApp(t *testing.T, lib *fakeLibrarian) *echo.Echo {
	t.Helper()
	app := echo.New()
	view, err := NewView()
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}
	app.Renderer = view
	svc := &fakeReading{doc: domain.Document{Title: "X", Path: "/x.md", HTML: "<p>x</p>"}}
	ReadingRoutes(app, NewReadingHandler(svc, "soul.demarkus.io", "/index.md").WithLibrarian(lib))
	return app
}

// fakeLibrarian scripts one Ask stream, records the questions asked, and
// serves a canned history.
type fakeLibrarian struct {
	events   []domain.LibrarianEvent
	history  []domain.LibrarianExchange
	askErr   error
	asked    []string
	contexts []string
}

func (f *fakeLibrarian) Ask(_ context.Context, _, question, trailContext string) (<-chan domain.LibrarianEvent, error) {
	f.asked = append(f.asked, question)
	f.contexts = append(f.contexts, trailContext)
	if f.askErr != nil {
		return nil, f.askErr
	}
	ch := make(chan domain.LibrarianEvent, len(f.events))
	for _, ev := range f.events {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

func (f *fakeLibrarian) History(string) []domain.LibrarianExchange { return f.history }

// askToken POSTs a question through /a/ask (htmx) and extracts the one-shot
// stream URL from the returned exchange fragment — the full token handoff.
func askToken(t *testing.T, e *echo.Echo, question string) string {
	t.Helper()
	form := url.Values{"question": {question}, "trail": {"a"}, "idx": {"0"}}
	req := httptest.NewRequest(http.MethodPost, "/a/ask", strings.NewReader(form.Encode()))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ask status = %d (body: %s)", rec.Code, rec.Body.String())
	}
	m := regexp.MustCompile(`sse-connect="([^"]+)"`).FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatalf("no sse-connect in fragment:\n%s", rec.Body.String())
	}
	return html.UnescapeString(m[1])
}

func TestLibrarianStream_LibrarianPathMapsEvents(t *testing.T) {
	t.Parallel()

	lib := &fakeLibrarian{events: []domain.LibrarianEvent{
		{Kind: domain.LibrarianTrace, Text: `open path="/ops/deploy.md"`},
		{Kind: domain.LibrarianToken, Text: "It lives "},
		{Kind: domain.LibrarianToken, Text: "in ops.\nSee the runbook."},
		{Kind: domain.LibrarianAnswer, Text: "It lives in ops.\nSee the runbook."},
		{Kind: domain.LibrarianDone},
	}}
	e := librarianApp(t, lib)

	streamURL := askToken(t, e, "where is it")
	if strings.Contains(streamURL, "where") {
		t.Fatalf("question leaked into the stream URL: %s", streamURL)
	}
	req := httptest.NewRequest(http.MethodGet, streamURL, http.NoBody)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	body := rec.Body.String()
	for _, want := range []string{
		"event: trace\ndata: open path=&#34;/ops/deploy.md&#34;",
		"event: token\ndata: It lives ",
		// A newline inside one event becomes two data: lines of one frame.
		"event: token\ndata: in ops.\ndata: See the runbook.",
		"event: answer\ndata: It lives in ops.\ndata: See the runbook.",
		// The rendered event carries the answer through the document
		// pipeline (the fake wraps in <p>) — unescaped HTML by design.
		"event: rendered\ndata: <p>It lives in ops.",
		"event: done",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\nbody:\n%s", want, body)
		}
	}
	if len(lib.asked) != 1 || lib.asked[0] != "where is it" {
		t.Errorf("asked = %v; want the question forwarded", lib.asked)
	}
	// The echo fallback must not have run alongside the real ask.
	if strings.Contains(body, "whispers back") {
		t.Errorf("echo fallback ran despite a wired librarian:\n%s", body)
	}

	// Tokens are one-shot: replaying the stream URL must not re-run the ask.
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, streamURL, http.NoBody))
	if len(lib.asked) != 1 {
		t.Errorf("replayed token re-ran the ask: %v", lib.asked)
	}
	if replay := rec.Body.String(); !strings.Contains(replay, "expired") || !strings.Contains(replay, "event: done") {
		t.Errorf("replayed token did not close gracefully:\n%s", replay)
	}
}

func TestLibrarianStream_BareQIsNeverARealAsk(t *testing.T) {
	t.Parallel()

	// A raw ?q= GET must never reach the model — the question-in-URL path
	// is gone (history/log hygiene); without a token or soak there is
	// nothing to stream.
	lib := &fakeLibrarian{}
	e := librarianApp(t, lib)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/a/stream?q=hi", http.NoBody))

	if len(lib.asked) != 0 {
		t.Errorf("bare ?q= reached the librarian: %v", lib.asked)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bare ?q= status = %d; want 400", rec.Code)
	}
}

func TestAskLibrarian_JunkIdxClamps(t *testing.T) {
	t.Parallel()

	// Regression: an out-of-range idx must clamp to a real pane (parseTrail
	// owns the rule) — the ask still hands off and streams.
	lib := &fakeLibrarian{events: []domain.LibrarianEvent{{Kind: domain.LibrarianDone}}}
	e := librarianApp(t, lib)

	form := url.Values{"question": {"clamped?"}, "trail": {"a"}, "idx": {"999"}}
	req := httptest.NewRequest(http.MethodPost, "/a/ask", strings.NewReader(form.Encode()))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("junk idx ask status = %d", rec.Code)
	}
	m := regexp.MustCompile(`sse-connect="([^"]+)"`).FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatal("no stream URL in fragment")
	}
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, html.UnescapeString(m[1]), http.NoBody))
	if !strings.Contains(rec.Body.String(), "event: done") {
		t.Errorf("clamped ask did not stream:\n%s", rec.Body.String())
	}
	if len(lib.asked) != 1 {
		t.Errorf("clamped ask did not run: %v", lib.asked)
	}
}

func TestLibrarianStream_ErrorPathIsGenericAndCloses(t *testing.T) {
	t.Parallel()

	// A stream that ends on Error with no Done — the port contract's other
	// terminal shape. The wire must carry a generic line (no internal error
	// text) and still close with a done frame.
	lib := &fakeLibrarian{events: []domain.LibrarianEvent{
		{Kind: domain.LibrarianError, Text: "provider.Stream: api error: status 400: secret-internal-detail"},
	}}
	e := librarianApp(t, lib)

	req := httptest.NewRequest(http.MethodGet, askToken(t, e, "boom"), http.NoBody)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "secret-internal-detail") {
		t.Errorf("internal error text reached the wire:\n%s", body)
	}
	if !strings.Contains(body, "event: trace\ndata: ⚠") {
		t.Errorf("missing generic error trace:\n%s", body)
	}
	if !strings.Contains(body, "event: done") {
		t.Errorf("error path did not close the stream with done:\n%s", body)
	}
}

func TestLibrarianStream_RejectsCrossSite(t *testing.T) {
	t.Parallel()

	lib := &fakeLibrarian{}
	e := librarianApp(t, lib)
	req := httptest.NewRequest(http.MethodGet, askToken(t, e, "steal tokens"), http.NoBody)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-site stream status = %d; want 403", rec.Code)
	}
	if len(lib.asked) != 0 {
		t.Errorf("cross-site request reached the librarian: %v", lib.asked)
	}
}

func TestLibrarianStream_SoakBypassesLibrarian(t *testing.T) {
	t.Parallel()

	lib := &fakeLibrarian{}
	e := librarianApp(t, lib)

	req := httptest.NewRequest(http.MethodGet, "/a/stream?slow=1", http.NoBody)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if len(lib.asked) != 0 {
		t.Errorf("soak reached the librarian: %v", lib.asked)
	}
	if ct := rec.Header().Get(echo.HeaderContentType); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q; want text/event-stream", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q; want no-store", cc)
	}
	if !strings.Contains(rec.Body.String(), "soak survived 1s") {
		t.Errorf("soak did not run:\n%s", rec.Body.String())
	}
}

func TestLibrarianStream_StopsOnClientDisconnect(t *testing.T) {
	t.Parallel()

	e := spikeApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/a/stream?slow=60", http.NoBody).WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		e.ServeHTTP(rec, req)
		close(done)
	}()
	// Let the stream start, then drop the client. The handler must return
	// promptly instead of ticking out the full soak.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream handler did not return after client disconnect")
	}
	if body := rec.Body.String(); strings.Contains(body, "event: done") {
		t.Errorf("disconnected stream still ran to completion:\n%s", body)
	}
}
