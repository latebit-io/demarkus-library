//go:build browser && unix

package web

import (
	"testing"
	"time"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// The presence section is empty markup until htmx fetches it: on the floor's
// own load, and in an activity pane a boosted click swapped in. A hidden tab
// fetches nothing, and asks again when the reader returns to it.
func TestBrowserPresenceFillsAfterThePage(t *testing.T) {
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
	b.eval(t, turn+`(true); document.querySelector('.presence').dispatchEvent(new Event('refresh'))`)
	// Long enough for a fetch that was not held back to reach the server.
	time.Sleep(300 * time.Millisecond)
	if got := here.calls.Load(); got != asked {
		t.Errorf("a hidden tab fetched presence %d times", got-asked)
	}
	b.eval(t, turn+`(false)`)
	deadline := time.Now().Add(5 * time.Second)
	for here.calls.Load() == asked && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if here.calls.Load() != asked+1 {
		t.Errorf("returning to the tab fetched presence %d times, want 1", here.calls.Load()-asked)
	}
}
