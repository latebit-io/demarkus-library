package web

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
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
	RoomRoutes(app, NewRoom(svc, "soul.demarkus.io", "/index.md").WithLibrarian(lib))
	return app
}

// fakeLibrarian scripts one Ask stream, records the questions asked, and
// serves a canned history.
type fakeLibrarian struct {
	mu       sync.Mutex // guards history: a resume polls it from the handler goroutine
	events   []domain.LibrarianEvent
	history  []domain.LibrarianExchange
	askErr   error
	resetErr error
	asked    []string
	contexts []string
	personas []domain.LibrarianPersona
	stopped  []string
	resets   []string
	// ctxs receives each Ask's context when set; release, when set, holds
	// the stream open (run in flight) until closed.
	ctxs    chan context.Context
	release chan struct{}
}

func (f *fakeLibrarian) Ask(ctx context.Context, ask domain.LibrarianAsk) (<-chan domain.LibrarianEvent, error) {
	f.asked = append(f.asked, ask.Question)
	f.contexts = append(f.contexts, ask.Context)
	f.personas = append(f.personas, ask.Persona)
	if f.ctxs != nil {
		f.ctxs <- ctx
	}
	if f.askErr != nil {
		return nil, f.askErr
	}
	ch := make(chan domain.LibrarianEvent, len(f.events))
	for _, ev := range f.events {
		ch <- ev
	}
	if f.release == nil {
		close(ch)
		return ch, nil
	}
	go func() {
		<-f.release
		close(ch)
	}()
	return ch, nil
}

func (f *fakeLibrarian) History(string) []domain.LibrarianExchange {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.history
}

func (f *fakeLibrarian) setHistory(history []domain.LibrarianExchange) {
	f.mu.Lock()
	f.history = history
	f.mu.Unlock()
}
func (f *fakeLibrarian) Stop(conversation string) { f.stopped = append(f.stopped, conversation) }
func (f *fakeLibrarian) Reset(conversation string) error {
	f.resets = append(f.resets, conversation)
	return f.resetErr
}

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
	m := regexp.MustCompile(`hx-sse:connect="([^"]+)"`).FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatalf("no hx-sse:connect in fragment:\n%s", rec.Body.String())
	}
	return html.UnescapeString(m[1])
}

func TestLibrarianStream_LibrarianPathMapsEvents(t *testing.T) {
	t.Parallel()

	deploy := domain.Ref{World: "w.io", Path: "/ops/deploy.md"}
	lib := &fakeLibrarian{events: []domain.LibrarianEvent{
		{Kind: domain.LibrarianTrace, Text: "opened", Ref: deploy},
		{Kind: domain.LibrarianSourceOpened, Text: "Deploy <runbook>", Ref: deploy},
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
	// Swapping frames are unnamed <hx-partial> events aimed at the exchange
	// block's regions (htmx 4 swaps only unnamed SSE messages); done stays a
	// named event.
	link := `<a href="/t/a/~/w.io/d/ops/deploy.md">w.io/ops/deploy.md</a>`
	for _, want := range []string{
		// Trace steps link the document they touched, continuing the trail.
		`data: <hx-partial hx-target="find .ask-trace" hx-swap="beforeend"><li>opened ` + link + "</li></hx-partial>\n\n",
		// The first token renders at once, through the document pipeline.
		`data: <hx-partial hx-target="find .ask-answer" hx-swap="innerHTML"><p>It lives </p></hx-partial>` + "\n\n",
		// A newline inside one event becomes two data: lines of one frame.
		`data: <hx-partial hx-target="find .ask-answer" hx-swap="innerHTML"><p>It lives in ops.` + "\ndata: See the runbook.</p></hx-partial>\n\n",
		// Settling: steps collapse, sources appear (escaped), the live
		// controls go, done closes the stream.
		`data: <hx-partial hx-target="find .ask-steps" hx-swap="outerHTML"><details class="ask-steps"><summary>1 step</summary>`,
		`data: <hx-partial hx-target="find .ask-sources" hx-swap="outerHTML"><p class="ask-sources">sources: <a href="/t/a/~/w.io/d/ops/deploy.md" title="w.io/ops/deploy.md">Deploy &lt;runbook&gt;</a></p></hx-partial>`,
		`data: <hx-partial hx-target="find .ask-live" hx-swap="outerHTML"></hx-partial>` + "\n\n",
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

	// A token starts one run: the same URL again (hx-sse reconnecting after
	// a drop) resumes it — never re-runs the ask — and renders the finished
	// exchange from History.
	lib.setHistory([]domain.LibrarianExchange{{
		Question: "where is it",
		Answer:   "It lives in ops.",
		Steps:    []domain.LibrarianStep{{Text: "opened", Ref: deploy}},
		Sources:  []domain.LibrarianSource{{Ref: deploy, Title: "Deploy <runbook>"}},
	}})
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, streamURL, http.NoBody))
	if len(lib.asked) != 1 {
		t.Errorf("replayed token re-ran the ask: %v", lib.asked)
	}
	replay := rec.Body.String()
	for _, want := range []string{
		"reconnected",
		`<hx-partial hx-target="find .ask-answer" hx-swap="innerHTML"><p>It lives in ops.</p></hx-partial>`,
		`<summary>1 step</summary>`,
		`Deploy &lt;runbook&gt;</a></p></hx-partial>`,
		"event: done",
	} {
		if !strings.Contains(replay, want) {
			t.Errorf("resumed stream missing %q:\n%s", want, replay)
		}
	}
	if strings.Contains(replay, "expired") {
		t.Errorf("resumed stream reported the ask expired:\n%s", replay)
	}
}

func TestLibrarianStream_UnknownTokenExpires(t *testing.T) {
	t.Parallel()

	lib := &fakeLibrarian{}
	e := librarianApp(t, lib)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/a/stream?ask=deadbeef", http.NoBody))
	if body := rec.Body.String(); !strings.Contains(body, "expired") || !strings.Contains(body, "event: done") {
		t.Errorf("unknown token did not close gracefully:\n%s", body)
	}
	if len(lib.asked) != 0 {
		t.Errorf("unknown token ran an ask: %v", lib.asked)
	}
}

func TestLibrarianStream_RunOutlivesDroppedConnection(t *testing.T) {
	t.Parallel()

	// The run's context is detached from the request: a dropped stream is
	// a reconnect in waiting, not the reader leaving.
	lib := &fakeLibrarian{
		events:  []domain.LibrarianEvent{{Kind: domain.LibrarianToken, Text: "working"}},
		ctxs:    make(chan context.Context, 1),
		release: make(chan struct{}),
	}
	e := librarianApp(t, lib)
	streamURL := askToken(t, e, "slow one")

	reqCtx, drop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, streamURL, http.NoBody).WithContext(reqCtx))
		close(done)
	}()
	runCtx := <-lib.ctxs
	drop()
	time.Sleep(50 * time.Millisecond)
	if runCtx.Err() != nil {
		t.Fatalf("run ended with the connection: %v", runCtx.Err())
	}
	close(lib.release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream handler did not return after the run ended")
	}
}

func TestLibrarianStream_ResumeWaitsForTheExchange(t *testing.T) {
	t.Parallel()

	// A reconnect while the run is still working waits for it to end, then
	// renders the exchange History gained.
	lib := &fakeLibrarian{
		events:  []domain.LibrarianEvent{{Kind: domain.LibrarianToken, Text: "working"}},
		ctxs:    make(chan context.Context, 1),
		release: make(chan struct{}),
	}
	e := librarianApp(t, lib)
	streamURL := askToken(t, e, "q")
	first := make(chan struct{})
	go func() {
		e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, streamURL, http.NoBody))
		close(first)
	}()
	<-lib.ctxs // the first stream owns the run before the reconnect arrives

	rec := httptest.NewRecorder()
	resumed := make(chan struct{})
	go func() {
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, streamURL, http.NoBody))
		close(resumed)
	}()
	select {
	case <-resumed:
		t.Fatalf("resume settled before the run ended:\n%s", rec.Body.String())
	case <-time.After(50 * time.Millisecond):
	}
	lib.setHistory([]domain.LibrarianExchange{{Question: "q", Answer: "late answer"}})
	close(lib.release)
	for _, done := range []chan struct{}{first, resumed} {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("stream did not settle once the run ended")
		}
	}
	if body := rec.Body.String(); !strings.Contains(body, "<p>late answer</p>") || !strings.Contains(body, "event: done") {
		t.Errorf("resume did not render the exchange:\n%s", body)
	}
}

func TestSSEWriter_KeepaliveUntilClosed(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var buf strings.Builder
	w := &lockedWriter{mu: &mu, w: &buf}
	out := newSSEWriter(context.Background(), w, func() {}, 5*time.Millisecond)
	time.Sleep(30 * time.Millisecond)
	out.close()
	mu.Lock()
	n := strings.Count(buf.String(), ": keepalive\n\n")
	mu.Unlock()
	if n == 0 {
		t.Fatal("no keepalive frames written")
	}
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if strings.Count(buf.String(), ": keepalive\n\n") != n {
		t.Error("keepalive kept writing after close")
	}
}

// lockedWriter lets the test read the buffer while the keepalive writes it.
type lockedWriter struct {
	mu *sync.Mutex
	w  *strings.Builder
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
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
	m := regexp.MustCompile(`hx-sse:connect="([^"]+)"`).FindStringSubmatch(rec.Body.String())
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
	if !strings.Contains(body, "hx-swap=\"beforeend\"><li>⚠") {
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
