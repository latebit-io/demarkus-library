package web

import (
	"context"
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// pathReaders answers each source from its own script.
type pathReaders map[string]*scriptedReader

func (r pathReaders) OpenCached(ctx context.Context, world, path string) (domain.Document, error) {
	return r[path].Open(ctx, world, path)
}

// sourceReaders scripts each path's reads, failing any read made before the
// stream subscribed through w.
func sourceReaders(w *fakeWatcher, results map[string][]readResult) pathReaders {
	readers := pathReaders{}
	for path, script := range results {
		readers[path] = &scriptedReader{watcher: w, results: script}
	}
	return readers
}

func edition(v string) readResult { return readResult{doc: domain.Document{Version: v}} }

// The answer read the deploy runbook at edition 4 and the rollback at 2.
var answeredFrom = []domain.LibrarianExchange{{Question: "how?", Answer: "So.", Sources: []domain.LibrarianSource{
	{Ref: domain.Ref{World: "w.io", Path: "/deploy.md"}, Title: "Deploy runbook", Version: "4"},
	{Ref: domain.Ref{World: "w.io", Path: "/rollback.md"}, Title: "Rollback", Version: "2"},
}}}

func TestLiveSourcesStream(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		results   map[string][]readResult
		signals   map[string]int
		wantNote  string // "" ⇒ no note at all
		wantDone  bool
		wantReads map[string]int // nil ⇒ unchecked
	}{
		{name: "unchanged sources say nothing",
			results: map[string][]readResult{"/deploy.md": {edition("4")}, "/rollback.md": {edition("2")}},
			signals: map[string]int{"/deploy.md": 1}},
		{name: "a source that moves on is named",
			results: map[string][]readResult{"/deploy.md": {edition("4"), edition("5")}, "/rollback.md": {edition("2")}},
			signals: map[string]int{"/deploy.md": 1}, wantNote: "Changed since the librarian read them: Deploy runbook"},
		{name: "a source that moved while away is named at once",
			results:  map[string][]readResult{"/deploy.md": {edition("4")}, "/rollback.md": {edition("3")}},
			wantNote: "Changed since the librarian read them: Rollback"},
		{name: "every source moved on ends the stream",
			results:  map[string][]readResult{"/deploy.md": {edition("5")}, "/rollback.md": {{err: domain.ErrArchived}}},
			wantNote: "Changed since the librarian read them: Deploy runbook, Rollback", wantDone: true},
		{name: "refused at connect ends the stream",
			results:  map[string][]readResult{"/deploy.md": {{err: domain.ErrUnauthorized}}, "/rollback.md": {edition("2")}},
			wantDone: true},
		{name: "refused later closes for a reconnect",
			results: map[string][]readResult{"/deploy.md": {edition("4"), {err: domain.ErrUnauthorized}}, "/rollback.md": {edition("2")}},
			signals: map[string]int{"/deploy.md": 2}, wantReads: map[string]int{"/deploy.md": 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			watcher := &fakeWatcher{byPath: map[string]int{}}
			maps.Copy(watcher.byPath, tt.signals)
			readers := sourceReaders(watcher, tt.results)
			h := LiveHandler{watcher: watcher, history: &fakeLibrarian{history: answeredFrom}, sources: readers}
			rec := get(liveApp(h), LiveSourcesPath)
			body := rec.Body.String()
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", rec.Code, body)
			}
			if got := editionFrames(body) > 0; got != (tt.wantNote != "") {
				t.Errorf("note sent = %v, want %v:\n%s", got, tt.wantNote != "", body)
			}
			if tt.wantNote != "" && !strings.Contains(body, tt.wantNote) {
				t.Errorf("body lacks %q:\n%s", tt.wantNote, body)
			}
			if got := strings.Contains(body, "event: done"); got != tt.wantDone {
				t.Errorf("done sent = %v, want %v", got, tt.wantDone)
			}
			for path, want := range tt.wantReads {
				if got := readers[path].reads; got != want {
					t.Errorf("%s reads = %d, want %d", path, got, want)
				}
			}
		})
	}
}

func TestLiveSourcesRefusals(t *testing.T) {
	t.Parallel()
	unknown := &fakeLibrarian{history: []domain.LibrarianExchange{{Sources: []domain.LibrarianSource{{Ref: domain.Ref{World: "w.io", Path: "/x.md"}, Title: "X"}}}}}
	tests := []struct {
		name string
		h    LiveHandler
		want int
	}{
		{"no change feed", LiveHandler{history: &fakeLibrarian{history: answeredFrom}}, http.StatusNotFound},
		{"no librarian", LiveHandler{watcher: &fakeWatcher{}}, http.StatusNotFound},
		{"no source of known edition", LiveHandler{watcher: &fakeWatcher{}, history: unknown}, http.StatusNotFound},
	}
	for _, tt := range tests {
		if rec := get(liveApp(tt.h), LiveSourcesPath); rec.Code != tt.want {
			t.Errorf("%s: status = %d, want %d", tt.name, rec.Code, tt.want)
		}
	}
}

// A source read by several answers is watched once, at the edition the latest
// one read.
func TestAnswerSourcesKeepTheLatestEdition(t *testing.T) {
	ref := domain.Ref{World: "w.io", Path: "/deploy.md"}
	got := answerSources([]domain.LibrarianExchange{
		{Sources: []domain.LibrarianSource{{Ref: ref, Title: "Deploy", Version: "4"}}},
		{Sources: []domain.LibrarianSource{{Ref: ref, Title: "Deploy", Version: "6"}}},
	})
	if len(got) != 1 || got[0].Version != "6" {
		t.Errorf("sources = %+v, want one at edition 6", got)
	}
}

// The pane always carries the stale-answer slot, so a pane's first answer has
// somewhere to start its stream; the stream itself needs a change feed.
func TestLibrarianPaneCarriesTheStaleSlot(t *testing.T) {
	tests := []struct {
		name    string
		history []domain.LibrarianExchange
		live    bool
		want    string
	}{
		{"no change feed", answeredFrom, false, `<div id="librarian-stale"></div>`},
		{"no answer yet", nil, true, `<div id="librarian-stale"></div>`},
		{"answered", answeredFrom, true, `<div id="librarian-stale"><p class="live-edition" role="status" hx-sse:connect="/live/sources"`},
	}
	for _, tt := range tests {
		if body := get(roomApp(t, liveRoom(&fakeLibrarian{history: tt.history}, tt.live)), "/t/a").Body.String(); !strings.Contains(body, tt.want) {
			t.Errorf("%s: pane lacks %q", tt.name, tt.want)
		}
	}
}

// liveRoom is librarianRoom, with a change feed when live.
func liveRoom(lib *fakeLibrarian, live bool) Room {
	if live {
		return librarianRoom(lib).WithLiveEditions(&fakeWatcher{})
	}
	return librarianRoom(lib)
}

// A settling answer refills the slot when the conversation has a source of
// known edition, before done closes its stream.
func TestSettledAnswerRefillsTheStaleSlot(t *testing.T) {
	tests := []struct {
		name    string
		history []domain.LibrarianExchange
		live    bool
		want    bool
	}{
		{"answered from known editions", answeredFrom, true, true},
		{"no change feed", answeredFrom, false, false},
		{"no source of known edition", nil, true, false},
	}
	refill := `data: <hx-partial hx-target="#librarian-stale" hx-swap="innerHTML"><p class="live-edition" role="status" hx-sse:connect="/live/sources"`
	for _, tt := range tests {
		lib := &fakeLibrarian{history: tt.history, events: []domain.LibrarianEvent{
			{Kind: domain.LibrarianAnswer, Text: "So."}, {Kind: domain.LibrarianDone},
		}}
		e := roomApp(t, liveRoom(lib, tt.live))
		body := get(e, askToken(t, e, "how?")).Body.String()
		at := strings.Index(body, refill)
		if got := at >= 0; got != tt.want {
			t.Errorf("%s: refilled = %v, want %v:\n%s", tt.name, got, tt.want, body)
		}
		if at > strings.Index(body, "event: done") {
			t.Errorf("%s: the slot refilled after done", tt.name)
		}
	}
}
