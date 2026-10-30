package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	RoomRoutes(app, librarianRoom(lib))
	return app
}

// librarianRoom is a room with a scripted librarian over one document.
func librarianRoom(lib *fakeLibrarian) Room {
	svc := &fakeReading{doc: domain.Document{Title: "X", Path: "/x.md", HTML: "<p>x</p>"}}
	return NewRoom(svc, "soul.demarkus.io", "/index.md").WithLibrarian(lib)
}

// fakeLibrarian scripts one Ask stream and one Attach stream, records the
// questions asked and the runs attached to, and serves a canned history.
type fakeLibrarian struct {
	events   []domain.LibrarianEvent
	history  []domain.LibrarianExchange
	askErr   error
	resetErr error
	asked    []string
	askIDs   []string
	contexts []string
	personas []domain.LibrarianPersona
	stopped  []string
	resets   []string
	// ctxs receives each Ask's context when set; release, when set, holds
	// the stream open (run in flight) until closed or the subscription's
	// context ends, as the adapter's subscriptions do.
	ctxs    chan context.Context
	release chan struct{}
	// attachEvents is what Attach replays; nil means it finds no run.
	// attached records each conversation/ask pair it was called with.
	attachEvents []domain.LibrarianEvent
	attached     []string
}

func (f *fakeLibrarian) Ask(ctx context.Context, ask domain.LibrarianAsk) (<-chan domain.LibrarianEvent, error) {
	f.asked = append(f.asked, ask.Question)
	f.askIDs = append(f.askIDs, ask.ID)
	f.contexts = append(f.contexts, ask.Context)
	f.personas = append(f.personas, ask.Persona)
	if f.ctxs != nil {
		f.ctxs <- ctx
	}
	if f.askErr != nil {
		return nil, f.askErr
	}
	return f.subscription(ctx, f.events), nil
}

func (f *fakeLibrarian) Attach(ctx context.Context, conversation, askID string) (<-chan domain.LibrarianEvent, bool) {
	f.attached = append(f.attached, conversation+"/"+askID)
	if f.attachEvents == nil {
		return nil, false
	}
	return f.subscription(ctx, f.attachEvents), true
}

// subscription serves events, then closes: at once after a done or when no
// run is held, else on release or ctx's end.
func (f *fakeLibrarian) subscription(ctx context.Context, events []domain.LibrarianEvent) <-chan domain.LibrarianEvent {
	ch := make(chan domain.LibrarianEvent, len(events))
	for _, ev := range events {
		ch <- ev
	}
	if f.release == nil || (len(events) > 0 && events[len(events)-1].Kind == domain.LibrarianDone) {
		close(ch)
		return ch
	}
	go func() {
		defer close(ch)
		select {
		case <-f.release:
		case <-ctx.Done():
		}
	}()
	return ch
}

func (f *fakeLibrarian) History(string) []domain.LibrarianExchange { return f.history }

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
	stream := streamURL(rec.Body.String())
	if stream == "" {
		t.Fatalf("no hx-sse:connect in fragment:\n%s", rec.Body.String())
	}
	return stream
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

	if len(lib.askIDs) != 1 || lib.askIDs[0] == "" {
		t.Errorf("ask ids = %v; want the token as the run's id", lib.askIDs)
	}
	// A settled stream forgets its token: nothing is left to attach to.
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, streamURL, http.NoBody))
	if !strings.Contains(rec.Body.String(), "expired") || len(lib.attached) != 0 {
		t.Errorf("settled token was still attachable:\n%s", rec.Body.String())
	}
}

// dropFirstStream opens the ask's stream and drops the connection while its
// run is held in flight, leaving the token attachable.
func dropFirstStream(t *testing.T, e *echo.Echo, lib *fakeLibrarian, streamURL string) {
	t.Helper()
	reqCtx, drop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, streamURL, http.NoBody).WithContext(reqCtx))
		close(done)
	}()
	<-lib.ctxs
	drop()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream handler did not return after the connection dropped")
	}
}

func TestLibrarianStream_ReconnectAttaches(t *testing.T) {
	t.Parallel()

	deploy := domain.Ref{World: "w.io", Path: "/ops/deploy.md"}
	lib := &fakeLibrarian{
		events:  []domain.LibrarianEvent{{Kind: domain.LibrarianToken, Text: "working"}},
		ctxs:    make(chan context.Context, 1),
		release: make(chan struct{}),
		attachEvents: []domain.LibrarianEvent{
			{Kind: domain.LibrarianTrace, Text: "opened", Ref: deploy},
			{Kind: domain.LibrarianSourceOpened, Text: "Deploy <runbook>", Ref: deploy},
			{Kind: domain.LibrarianAnswer, Text: "It lives in ops."},
			{Kind: domain.LibrarianDone},
		},
	}
	defer close(lib.release)
	e := librarianApp(t, lib)
	streamURL := askToken(t, e, "where is it")
	dropFirstStream(t, e, lib, streamURL)

	// The same URL again (hx-sse reconnecting) attaches to the run — never
	// re-runs the ask — clears the steps already shown, and streams the replay.
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, streamURL, http.NoBody))
	if len(lib.asked) != 1 {
		t.Errorf("reconnect re-ran the ask: %v", lib.asked)
	}
	if len(lib.attached) != 1 || !strings.HasSuffix(lib.attached[0], "/"+lib.askIDs[0]) {
		t.Errorf("attached = %v; want the run of ask %q", lib.attached, lib.askIDs[0])
	}
	link := `<a href="/t/a/~/w.io/d/ops/deploy.md">w.io/ops/deploy.md</a>`
	replay := rec.Body.String()
	for _, want := range []string{
		`<hx-partial hx-target="find .ask-trace" hx-swap="innerHTML"></hx-partial>`,
		`<hx-partial hx-target="find .ask-trace" hx-swap="beforeend"><li>opened ` + link + "</li></hx-partial>",
		`<hx-partial hx-target="find .ask-answer" hx-swap="innerHTML"><p>It lives in ops.</p></hx-partial>`,
		`<summary>1 step</summary>`,
		`Deploy &lt;runbook&gt;</a></p></hx-partial>`,
		"event: done",
	} {
		if !strings.Contains(replay, want) {
			t.Errorf("attached stream missing %q:\n%s", want, replay)
		}
	}
}

func TestLibrarianStream_SourcesFrameOffersOpenAll(t *testing.T) {
	t.Parallel()

	// Two messages in one run: only the final message's citations lay out.
	deploy := domain.Ref{World: "w.io", Path: "/ops/deploy.md"}
	lib := &fakeLibrarian{events: []domain.LibrarianEvent{
		{Kind: domain.LibrarianAnswer, Text: `Checking <a href="mark://w.io/ops/draft.md">a draft</a>.`},
		{Kind: domain.LibrarianSourceOpened, Text: "Deploy runbook", Ref: deploy},
		{Kind: domain.LibrarianAnswer, Text: `See <a href="mark://w.io/ops/deploy.md">deploy</a>.`},
		{Kind: domain.LibrarianDone},
	}}
	e := librarianApp(t, lib)
	streamURL := askToken(t, e, "what next")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, streamURL, http.NoBody))
	body := rec.Body.String()
	// The markup itself is pinned by the pane test; here only which link the
	// streamed sources frame carries.
	if want := `<a class="ask-open" href="/t/a/~/w.io/d/ops/deploy.md">`; !strings.Contains(body, want) {
		t.Errorf("sources frame missing %q:\n%s", want, body)
	}
	if strings.Contains(body, `class="ask-open" href="/t/a/~/w.io/d/ops/draft.md`) {
		t.Errorf("sources frame kept the earlier message's citation:\n%s", body)
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

func TestLibrarianStream_DroppedConnectionReturns(t *testing.T) {
	t.Parallel()

	// The subscription is scoped to the request: a drop ends the handler
	// while the run, the librarian's concern, carries on.
	lib := &fakeLibrarian{
		events:  []domain.LibrarianEvent{{Kind: domain.LibrarianToken, Text: "working"}},
		ctxs:    make(chan context.Context, 1),
		release: make(chan struct{}),
	}
	defer close(lib.release)
	e := librarianApp(t, lib)
	dropFirstStream(t, e, lib, askToken(t, e, "slow one"))
}

func TestLibrarianStream_ReconnectWithoutRunSettles(t *testing.T) {
	t.Parallel()

	lib := &fakeLibrarian{ctxs: make(chan context.Context, 1), release: make(chan struct{})}
	defer close(lib.release)
	e := librarianApp(t, lib)
	streamURL := askToken(t, e, "q")
	dropFirstStream(t, e, lib, streamURL)

	// The librarian no longer holds the run (evicted, or a newer ask took
	// its place): the reconnect says so and closes.
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, streamURL, http.NoBody))
	if body := rec.Body.String(); !strings.Contains(body, "lost track of this answer") || !strings.Contains(body, "event: done") {
		t.Errorf("reconnect without a run did not settle:\n%s", body)
	}
	if len(lib.attached) != 1 {
		t.Errorf("attached = %v; want one attempt", lib.attached)
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
	stream := streamURL(rec.Body.String())
	if stream == "" {
		t.Fatal("no stream URL in fragment")
	}
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, stream, http.NoBody))
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
