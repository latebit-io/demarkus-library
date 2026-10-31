//go:build browser && unix

package web

import (
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// With the local graph open, a write to a neighbour pulses it in place and
// an archive fades one, without redrawing the graph.
func TestBrowserLiveGraphPulsesAndFades(t *testing.T) {
	written := pulseOf(domain.OpAppend, "w.io", "/b.md", 2)
	live := &fakeWatcher{
		// The pane's edition stream ends at once: the test server serves one request at a time.
		signals:      signalsThenClose(0),
		pulseSignals: 2,
		pulses:       [][]domain.Pulse{{}, {written}, {pulseOf(domain.OpArchive, "w.io", "/c.md", 3), written}},
	}
	srv := browserServer(t, roomApp(t, NewRoom(graphFixture(), "soul.demarkus.io", "/index.md").WithLiveEditions(live)))
	b := launchBrowser(t)
	b.navigate(t, srv.URL+"/t/w.io/d/a.md")
	b.key(t, "g")
	b.eval(t, `window.drawn = document.querySelector('#graph-overlay svg')`)
	b.waitFor(t, `document.querySelector('#graph-overlay svg a[data-node="/b.md"].node-pulse')`)
	b.waitFor(t, `document.querySelector('#graph-overlay svg a[data-node="/c.md"].gone')`)
	if b.eval(t, `document.querySelector('#graph-overlay svg') === window.drawn`) != true {
		t.Error("the graph was redrawn; the reader's view would reset")
	}
}
