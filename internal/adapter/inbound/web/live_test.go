package web

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// fakeWatcher hands out one scripted signal channel and records what the
// stream watched.
type fakeWatcher struct {
	mu      sync.Mutex
	signals chan struct{}
	watched []string
}

func (f *fakeWatcher) WatchDocument(_ context.Context, world, path string) <-chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.watched = append(f.watched, world+path)
	return f.signals
}

// signalsThenClose is a watcher channel holding n signals, closed after them
// so the stream ends once it has drained them.
func signalsThenClose(n int) chan struct{} {
	ch := make(chan struct{}, n)
	for range n {
		ch <- struct{}{}
	}
	close(ch)
	return ch
}

// readResult is one scripted answer to Read.
type readResult struct {
	doc domain.Document
	err error
}

// scriptedReader answers each Read with the next scripted result, repeating
// the last; it fails a read made before the watcher subscribed.
type scriptedReader struct {
	watcher *fakeWatcher
	results []readResult
	reads   int
}

func (r *scriptedReader) Read(context.Context, string, string) (domain.Document, error) {
	r.watcher.mu.Lock()
	subscribed := len(r.watcher.watched) > 0
	r.watcher.mu.Unlock()
	if !subscribed {
		return domain.Document{}, errors.New("read before subscribe: a write in between would be missed")
	}
	result := r.results[min(r.reads, len(r.results)-1)]
	r.reads++
	return result.doc, result.err
}

func liveApp(h LiveHandler) *echo.Echo {
	app := echo.New()
	liveRoutes(app, h)
	return app
}

func liveGet(app *echo.Echo, target string, header http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, http.NoBody)
	maps.Copy(req.Header, header)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	return rec
}

const liveTarget = "/live?world=soul.demarkus.io&path=%2Fx.md&v=1"

func editionFrames(body string) int {
	return strings.Count(body, `hx-target="find .edition-text"`)
}

func TestLiveEditionStream(t *testing.T) {
	t.Parallel()
	v1 := readResult{doc: domain.Document{Version: "1"}}
	v2 := readResult{doc: domain.Document{Version: "2", Agent: "claude-code", User: "fritz@latebit.io"}}
	tests := []struct {
		name       string
		results    []readResult
		signals    int
		wantFrames int
		wantText   string
		wantDone   bool
		wantReads  int // 0 = unchecked
	}{
		{name: "newer edition after a hint", results: []readResult{v1, v2},
			signals: 1, wantFrames: 1, wantText: "Edition 2 published by fritz@latebit.io"},
		{name: "edition missed while disconnected", results: []readResult{v2},
			signals: 0, wantFrames: 1, wantText: "Edition 2 published by fritz@latebit.io"},
		{name: "same edition says nothing", results: []readResult{v1},
			signals: 2, wantFrames: 0},
		{name: "transient error keeps the stream", results: []readResult{{err: errors.New("timeout")}, v2},
			signals: 1, wantFrames: 1, wantText: "Edition 2"},
		{name: "archived ends the stream", results: []readResult{{err: domain.ErrArchived}},
			signals: 3, wantFrames: 1, wantText: "Archived since you opened it", wantDone: true},
		{name: "removed ends the stream", results: []readResult{{err: domain.ErrNotFound}},
			signals: 0, wantFrames: 1, wantText: "Removed since you opened it", wantDone: true},
		{name: "refused at connect ends the stream", results: []readResult{{err: domain.ErrUnauthorized}},
			signals: 3, wantFrames: 0, wantDone: true, wantReads: 1},
		{name: "refused later closes for a reconnect", results: []readResult{v1, {err: domain.ErrUnauthorized}},
			signals: 3, wantFrames: 0, wantDone: false, wantReads: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			watcher := &fakeWatcher{signals: signalsThenClose(tt.signals)}
			reader := &scriptedReader{watcher: watcher, results: tt.results}
			rec := get(liveApp(LiveHandler{reader: reader, watcher: watcher}), liveTarget)

			body := rec.Body.String()
			if ct := rec.Header().Get(echo.HeaderContentType); ct != "text/event-stream" {
				t.Fatalf("Content-Type = %q (body %s)", ct, body)
			}
			if got := editionFrames(body); got != tt.wantFrames {
				t.Errorf("edition frames = %d, want %d:\n%s", got, tt.wantFrames, body)
			}
			if !strings.Contains(body, tt.wantText) {
				t.Errorf("body lacks %q:\n%s", tt.wantText, body)
			}
			if got := strings.Contains(body, "event: done"); got != tt.wantDone {
				t.Errorf("done sent = %v, want %v", got, tt.wantDone)
			}
			if tt.wantReads > 0 && reader.reads != tt.wantReads {
				t.Errorf("reads = %d, want %d", reader.reads, tt.wantReads)
			}
			if watcher.watched[0] != "soul.demarkus.io/x.md" {
				t.Errorf("watched %v", watcher.watched)
			}
		})
	}
}

// Banner text is escaped: a writer name is data, never markup.
func TestLiveEditionEscapesWriter(t *testing.T) {
	t.Parallel()
	watcher := &fakeWatcher{signals: signalsThenClose(0)}
	reader := &scriptedReader{watcher: watcher,
		results: []readResult{{doc: domain.Document{Version: "2", Agent: `<img src=x onerror=alert(1)>`}}}}
	body := get(liveApp(LiveHandler{reader: reader, watcher: watcher}), liveTarget).Body.String()
	if strings.Contains(body, "<img") || !strings.Contains(body, "&lt;img") {
		t.Errorf("writer not escaped:\n%s", body)
	}
}

func TestLiveEditionRefusals(t *testing.T) {
	t.Parallel()
	crossSite := http.Header{"Sec-Fetch-Site": {"cross-site"}}
	tests := []struct {
		name    string
		target  string
		header  http.Header
		watcher bool
		want    int
	}{
		{"cross-site fetch", liveTarget, crossSite, true, http.StatusForbidden},
		{"no change feed", liveTarget, nil, false, http.StatusNotFound},
		{"listing", "/live?world=w.io&path=%2Fplans%2F&v=1", nil, true, http.StatusBadRequest},
		{"pinned edition", "/live?world=w.io&path=%2Fx.md%2Fv3&v=3", nil, true, http.StatusBadRequest},
		{"no shown edition", "/live?world=w.io&path=%2Fx.md", nil, true, http.StatusBadRequest},
		{"no world", "/live?path=%2Fx.md&v=1", nil, true, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := LiveHandler{reader: &scriptedReader{}}
			if tt.watcher {
				h.watcher = &fakeWatcher{signals: signalsThenClose(0)}
			}
			if rec := liveGet(liveApp(h), tt.target, tt.header); rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

// Only the focused document pane of a room with live editions connects a
// stream, carrying the edition it rendered.
func TestCanvasLiveEditionStream(t *testing.T) {
	t.Parallel()
	doc := domain.Document{Title: "X", Path: "/x.md", HTML: "<p>x</p>", Version: "3"}
	room := func(live bool) *echo.Echo {
		r := NewRoom(&fakeReading{doc: doc}, "soul.demarkus.io", "/index.md")
		if live {
			r = r.WithLiveEditions(&fakeWatcher{signals: signalsThenClose(0)})
		}
		return roomApp(t, r)
	}
	const stream = `hx-sse:connect="/live?path=%2Fx.md&amp;v=3&amp;world=soul.demarkus.io"`
	tests := []struct {
		name   string
		live   bool
		target string
		want   int
	}{
		{"focused document", true, "/t/soul.demarkus.io/d/x.md", 1},
		{"two panes, one stream", true, "/t/soul.demarkus.io/d/x.md/~/soul.demarkus.io/d/x.md", 1},
		{"feature off", false, "/t/soul.demarkus.io/d/x.md", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := get(room(tt.live), tt.target)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			if got := strings.Count(rec.Body.String(), stream); got != tt.want {
				t.Errorf("streams = %d, want %d", got, tt.want)
			}
		})
	}
}
