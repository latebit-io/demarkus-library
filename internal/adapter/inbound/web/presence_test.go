package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

type fakePresence struct {
	people []domain.Presence
	err    error
	calls  atomic.Int32
}

func (f *fakePresence) Present(context.Context) ([]domain.Presence, error) {
	f.calls.Add(1)
	return f.people, f.err
}

func presenceRoom(p *fakePresence) Room {
	return NewRoom(&fakeReading{floor: testFloor()}, "soul.demarkus.io", "/index.md").WithPresence(p)
}

// getPresence asks for the fragment as the section's htmx does.
func getPresence(app *echo.Echo) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, PresencePath, http.NoBody)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	return rec
}

// Only the focused floor or activity pane fetches who is here, so one canvas
// makes one fetch; a room with no broker to ask shows no section.
func TestPresenceSectionIsFetchedByTheFocusedPane(t *testing.T) {
	const fetch = `hx-get="/presence"`
	with := roomApp(t, presenceRoom(&fakePresence{}))
	if body := get(with, "/t/u").Body.String(); !strings.Contains(body,
		`<section class="pulse presence" aria-label="Here lately" hx-get="/presence" hx-trigger="load, every 2m, refresh" hx-swap="innerHTML"></section>`) {
		t.Error("the focused floor lacks the presence section")
	}
	for path, want := range map[string]int{
		"/t/u":                1,
		"/t/v":                1,
		"/t/u/~/v":            1, // the activity pane holds the focus, not the floor
		"/t/u/~/v?focus=0":    1,
		"/t/v/~/w.io/d/x.md":  0, // a document holds the focus
		"/t/u/~/w.io/d/x.md":  0,
		"/t/w.io/d/x.md/~/v":  1,
		"/t/w.io/d/x.md/~/u":  1,
		"/t/w.io/d/x.md?x=no": 0,
	} {
		if got := strings.Count(get(with, path).Body.String(), fetch); got != want {
			t.Errorf("%s: %d presence sections, want %d", path, got, want)
		}
	}
	without := readingApp(t, &fakeReading{floor: testFloor()})
	if body := get(without, "/t/u").Body.String(); strings.Contains(body, fetch) {
		t.Error("a room without presence shows the section")
	}
}

func TestPresenceFragment(t *testing.T) {
	people := []domain.Presence{
		{User: "ana@example.org", Clients: []string{"claude-code", "demarkus-library"}, Seen: time.Date(2026, 11, 19, 13, 36, 0, 0, time.FixedZone("CET", 3600))},
		{User: "<b>@example.org", Clients: []string{"unknown"}, Seen: time.Date(2026, 11, 19, 12, 30, 0, 0, time.UTC)},
	}
	const unavailable = `<li class="pulse-quiet">The list is unavailable right now</li>`
	tests := []struct {
		name     string
		presence *fakePresence
		want     []string
	}{
		{"people", &fakePresence{people: people}, []string{
			`<h2 class="pulse-head">Here lately</h2>`,
			`<time datetime="2026-11-19T12:36:00Z">12:36 UTC</time>`,
			`<span class="pulse-by person">ana@example.org</span>`,
			`<span class="pulse-by agent" title="connected with">claude-code</span> <span class="pulse-by agent" title="connected with">demarkus-library</span>`,
			`<span class="pulse-by person">&lt;b&gt;@example.org</span>`,
		}},
		{"no one", &fakePresence{}, []string{`<li class="pulse-quiet">No one seen lately</li>`}},
		{"failed", &fakePresence{err: errors.New("broker down")}, []string{unavailable}},
		// Pinned: a refusal of this one tool must not send the reader to sign in.
		{"refused", &fakePresence{err: domain.ErrUnauthorized}, []string{unavailable}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := getPresence(roomApp(t, presenceRoom(tt.presence)))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			body := rec.Body.String()
			for _, want := range tt.want {
				if !strings.Contains(body, want) {
					t.Errorf("fragment lacks %q in:\n%s", want, body)
				}
			}
			if strings.Contains(body, "<html") {
				t.Error("fragment is a whole page")
			}
		})
	}
}

// What is no list answers with nothing to swap: no broker to ask, a broker
// without the tool, and a reader who landed on the fragment itself.
func TestPresenceFragmentWithoutAList(t *testing.T) {
	off := getPresence(readingApp(t, &fakeReading{floor: testFloor()}))
	if off.Code != http.StatusNotFound {
		t.Errorf("without presence: status = %d, want 404", off.Code)
	}
	old := getPresence(roomApp(t, presenceRoom(&fakePresence{err: domain.ErrPresenceUnsupported})))
	if old.Code != http.StatusNotFound || strings.Contains(old.Body.String(), "pulse-quiet") {
		t.Errorf("older broker: status = %d, want a 404 with no list", old.Code)
	}
	asked := &fakePresence{}
	direct := get(roomApp(t, presenceRoom(asked)), PresencePath)
	if direct.Code != http.StatusSeeOther || direct.Header().Get("Location") != "/" || asked.calls.Load() != 0 {
		t.Errorf("direct hit: status = %d to %q after %d calls, want a 303 home and no call",
			direct.Code, direct.Header().Get("Location"), asked.calls.Load())
	}
}
