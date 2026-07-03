package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// spikeApp wires the reading routes with no librarian — the /a/ surface in
// its feature-dark posture, where the stream is the transport-spike echo.
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
	events  []domain.LibrarianEvent
	history []domain.LibrarianExchange
	askErr  error
	asked   []string
}

func (f *fakeLibrarian) Ask(_ context.Context, _, question string) (<-chan domain.LibrarianEvent, error) {
	f.asked = append(f.asked, question)
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

func TestSpikeStream_LibrarianPathMapsEvents(t *testing.T) {
	t.Parallel()

	lib := &fakeLibrarian{events: []domain.LibrarianEvent{
		{Kind: domain.LibrarianTrace, Text: `open path="/ops/deploy.md"`},
		{Kind: domain.LibrarianToken, Text: "It lives "},
		{Kind: domain.LibrarianToken, Text: "in ops.\nSee the runbook."},
		{Kind: domain.LibrarianAnswer, Text: "It lives in ops.\nSee the runbook."},
		{Kind: domain.LibrarianDone},
	}}
	e := librarianApp(t, lib)

	req := httptest.NewRequest(http.MethodGet, "/a/stream?q=where+is+it", http.NoBody)
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
}

func TestSpikeStream_ErrorPathIsGenericAndCloses(t *testing.T) {
	t.Parallel()

	// A stream that ends on Error with no Done — the port contract's other
	// terminal shape. The wire must carry a generic line (no internal error
	// text) and still close with a done frame.
	lib := &fakeLibrarian{events: []domain.LibrarianEvent{
		{Kind: domain.LibrarianError, Text: "provider.Stream: api error: status 400: secret-internal-detail"},
	}}
	e := librarianApp(t, lib)

	req := httptest.NewRequest(http.MethodGet, "/a/stream?q=boom", http.NoBody)
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

func TestSpikeStream_RejectsCrossSite(t *testing.T) {
	t.Parallel()

	lib := &fakeLibrarian{}
	e := librarianApp(t, lib)
	req := httptest.NewRequest(http.MethodGet, "/a/stream?q=steal+tokens", http.NoBody)
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

func TestSpikeStream_SoakBypassesLibrarian(t *testing.T) {
	t.Parallel()

	lib := &fakeLibrarian{}
	e := librarianApp(t, lib)

	req := httptest.NewRequest(http.MethodGet, "/a/stream?slow=1", http.NoBody)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if len(lib.asked) != 0 {
		t.Errorf("soak reached the librarian: %v", lib.asked)
	}
	if !strings.Contains(rec.Body.String(), "soak survived 1s") {
		t.Errorf("soak did not run:\n%s", rec.Body.String())
	}
}

func TestSpikeStream_EmitsTraceTokensDone(t *testing.T) {
	t.Parallel()

	e := spikeApp(t)
	req := httptest.NewRequest(http.MethodGet, "/a/stream?q=hi", http.NoBody)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	if ct := rec.Header().Get(echo.HeaderContentType); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q; want text/event-stream", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q; want no-store", cc)
	}

	body := rec.Body.String()
	for _, want := range []string{
		"event: trace\ndata: lookup &#34;hi&#34;",
		"event: token\ndata: whispers ",
		"event: token\ndata: hi ",
		"event: done\n",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\nbody:\n%s", want, body)
		}
	}
	// The done frame must be last — the client closes the EventSource on it.
	if !strings.Contains(body[strings.LastIndex(body, "event:"):], "event: done") {
		t.Errorf("done is not the final event\nbody:\n%s", body)
	}
}

func TestSpikeStream_EscapesUserText(t *testing.T) {
	t.Parallel()

	e := spikeApp(t)
	req := httptest.NewRequest(http.MethodGet, "/a/stream?q="+
		"%3Cscript%3Ealert(1)%3C%2Fscript%3E", http.NoBody)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "<script>") {
		t.Errorf("unescaped script tag reached the SSE data:\n%s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("expected HTML-escaped question in stream data:\n%s", body)
	}
}

func TestSpikeStream_StopsOnClientDisconnect(t *testing.T) {
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

func TestSpikePage_WiresSSEExtension(t *testing.T) {
	t.Parallel()

	e := spikeApp(t)
	req := httptest.NewRequest(http.MethodGet, "/a/spike?q=where+is+the+runbook%3F", http.NoBody)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`src="/static/sse.min.js"`,
		`hx-ext="sse"`,
		`sse-connect="/a/stream?q=where+is+the+runbook%3F"`,
		`sse-close="done"`,
		`sse-swap="token"`,
		`value="where is the runbook?"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q\nbody:\n%s", want, body)
		}
	}
}

func TestSpikePage_EscapesQuestionInAttributes(t *testing.T) {
	t.Parallel()

	e := spikeApp(t)
	req := httptest.NewRequest(http.MethodGet, "/a/spike?q=%22%3E%3Cscript%3Ealert(1)%3C%2Fscript%3E", http.NoBody)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if body := rec.Body.String(); strings.Contains(body, "<script>alert") {
		t.Errorf("unescaped question broke out of the attribute:\n%s", body)
	}
}
