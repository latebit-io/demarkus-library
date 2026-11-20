//go:build browser && unix

package web

import (
	"testing"
	"time"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// The page lists who is here, a boosted click into the activity pane carries
// the list along, and htmx only refreshes it: a hidden tab fetches nothing,
// and asks again when the reader returns to it.
func TestBrowserPresenceRefreshesOnReturn(t *testing.T) {
	here := &fakePresence{people: []domain.Presence{
		{User: "ana@example.org", Clients: []string{"claude-code"}, Seen: time.Date(2026, 11, 19, 12, 36, 0, 0, time.UTC)},
	}}
	srv := browserServer(t, roomApp(t, presenceRoom(here).WithLiveEditions(&fakeWatcher{})))
	b := launchBrowser(t)
	const listed = `(function () {
		var who = document.querySelector('.pane.focused .presence .pulse-by.person');
		return !!who && who.textContent === 'ana@example.org';
	})()`
	b.navigate(t, srv.URL+"/t/u")
	b.waitFor(t, listed)

	b.eval(t, `window.kept = true; document.querySelector('a.nav-activity').click()`)
	b.waitFor(t, `document.querySelector('.pane.focused').dataset.kind === 'v'`)
	b.waitFor(t, listed)
	if b.eval(t, `window.kept`) != true {
		t.Error("opening the activity pane reloaded the page")
	}

	const turn = `(function (hidden) {
		Object.defineProperty(document, 'hidden', { configurable: true, get: function () { return hidden; } });
		document.dispatchEvent(new Event('visibilitychange'));
	})`
	asked := here.calls.Load()
	// Emptied, so the list can only come back by a fetch.
	b.eval(t, turn+`(true); (function (section) {
		section.innerHTML = '';
		section.dispatchEvent(new Event('refresh'));
	})(document.querySelector('.presence'))`)
	// Long enough for a fetch that was not held back to reach the server.
	time.Sleep(300 * time.Millisecond)
	if got := here.calls.Load(); got != asked {
		t.Errorf("a hidden tab fetched presence %d times", got-asked)
	}
	b.eval(t, turn+`(false)`)
	b.waitFor(t, listed)
	// Not an exact count: the section's own timer may fetch as well.
	if here.calls.Load() <= asked {
		t.Error("the list returned without a fetch")
	}
}
