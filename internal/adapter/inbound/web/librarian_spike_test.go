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

// spikeApp wires the spike routes into a bare Echo instance, mirroring the
// composition root's registration (no turnstile — quic-mode shape; nil
// librarian = the echo fallback).
func spikeApp() *echo.Echo {
	e := echo.New()
	LibrarianSpikeRoutes(e, nil)
	return e
}

// fakeLibrarian scripts one Ask stream and records the questions asked.
type fakeLibrarian struct {
	events []domain.LibrarianEvent
	asked  []string
}

func (f *fakeLibrarian) Ask(_ context.Context, _, question string) (<-chan domain.LibrarianEvent, error) {
	f.asked = append(f.asked, question)
	ch := make(chan domain.LibrarianEvent, len(f.events))
	for _, ev := range f.events {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

func TestSpikeStream_LibrarianPathMapsEvents(t *testing.T) {
	t.Parallel()

	lib := &fakeLibrarian{events: []domain.LibrarianEvent{
		{Kind: domain.LibrarianTrace, Text: `open path="/ops/deploy.md"`},
		{Kind: domain.LibrarianToken, Text: "It lives "},
		{Kind: domain.LibrarianToken, Text: "in ops.\nSee the runbook."},
		{Kind: domain.LibrarianAnswer, Text: "It lives in ops.\nSee the runbook."},
		{Kind: domain.LibrarianDone},
	}}
	e := echo.New()
	LibrarianSpikeRoutes(e, lib)

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

func TestSpikeStream_SoakBypassesLibrarian(t *testing.T) {
	t.Parallel()

	lib := &fakeLibrarian{}
	e := echo.New()
	LibrarianSpikeRoutes(e, lib)

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

	e := spikeApp()
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

	e := spikeApp()
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

	e := spikeApp()
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

	e := spikeApp()
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

	e := spikeApp()
	req := httptest.NewRequest(http.MethodGet, "/a/spike?q=%22%3E%3Cscript%3Ealert(1)%3C%2Fscript%3E", http.NoBody)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if body := rec.Body.String(); strings.Contains(body, "<script>alert") {
		t.Errorf("unescaped question broke out of the attribute:\n%s", body)
	}
}
