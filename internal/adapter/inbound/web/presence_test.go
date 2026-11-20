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

// The page itself lists who is here, so it reads without JS, and only on the
// focused floor or activity pane: one canvas asks the broker once.
func TestPresenceIsListedOnTheFocusedPane(t *testing.T) {
	const section = `<section class="pulse presence" aria-label="Here lately" hx-get="/presence" hx-trigger="every 2m, refresh" hx-swap="innerHTML">`
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
		t.Run(path, func(t *testing.T) {
			here := &fakePresence{people: []domain.Presence{{User: "ana@example.org", Clients: []string{"claude-code"}}}}
			body := get(roomApp(t, presenceRoom(here)), path).Body.String()
			listed := strings.Count(body, section+`<h2 class="pulse-head">Here lately</h2>`)
			people := strings.Count(body, `<span class="pulse-by person">ana@example.org</span>`)
			if listed != want || people != want || int(here.calls.Load()) != want {
				t.Errorf("%d sections listing %d people after %d calls, want %d of each", listed, people, here.calls.Load(), want)
			}
		})
	}
}

// A room with no list shows no section, so nothing polls for one; a list
// that failed shows its note on the page.
func TestPresenceSectionWithoutAList(t *testing.T) {
	const section = `class="pulse presence"`
	tests := []struct {
		name string
		app  *echo.Echo
		want string // "" for no section
	}{
		{"no broker to ask", readingApp(t, &fakeReading{floor: testFloor()}), ""},
		{"older broker", roomApp(t, presenceRoom(&fakePresence{err: domain.ErrPresenceUnsupported})), ""},
		{"failed", roomApp(t, presenceRoom(&fakePresence{err: errors.New("broker down")})), "The list is unavailable right now"},
		{"refused", roomApp(t, presenceRoom(&fakePresence{err: domain.ErrUnauthorized})), "The list is unavailable right now"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := get(tt.app, "/t/u")
			body := rec.Body.String()
			if rec.Code != http.StatusOK || strings.Contains(body, section) != (tt.want != "") || !strings.Contains(body, tt.want) {
				t.Errorf("status = %d, section shown = %v, want 200 and %q", rec.Code, strings.Contains(body, section), tt.want)
			}
		})
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
