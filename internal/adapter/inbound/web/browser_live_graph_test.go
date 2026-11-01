//go:build browser && unix

package web

import (
	"testing"
	"time"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// With the local graph open, a write to a neighbour pulses it in place, and
// an archive between the page and the overlay still fades its node, on each
// side it is drawn, all without redrawing the graph.
func TestBrowserLiveGraphPulsesAndFades(t *testing.T) {
	// After the drawing, which the page stamps when it renders.
	archived := pulseOf(domain.OpArchive, "w.io", "/c.md", 3)
	archived.At = time.Now().Add(time.Minute)
	written := pulseOf(domain.OpAppend, "w.io", "/b.md", 2)
	written.At = archived.At.Add(time.Minute)
	live := &fakeWatcher{
		// The pane's edition stream ends at once: the test server serves one request at a time.
		signals:      signalsThenClose(0),
		pulseSignals: 1,
		pulses:       [][]domain.Pulse{{archived}, {written, archived}},
	}
	// c links back too, so the graph draws it on both sides.
	fixture := graphFixture()
	centre := fixture.neighbor["/a.md"]
	centre.In = []domain.Ref{{World: "w.io", Path: "/c.md"}}
	fixture.neighbor["/a.md"] = centre
	srv := browserServer(t, roomApp(t, NewRoom(fixture, "soul.demarkus.io", "/index.md").WithLiveEditions(live)))
	b := launchBrowser(t)
	b.navigate(t, srv.URL+"/t/w.io/d/a.md")
	b.key(t, "g")
	b.eval(t, `window.drawn = document.querySelector('#graph-overlay svg')`)
	b.waitFor(t, `document.querySelector('#graph-overlay svg a[data-node="/b.md"].node-pulse')`)
	b.waitFor(t, `document.querySelectorAll('#graph-overlay svg a[data-node="/c.md"].gone').length === 2`)
	if b.eval(t, `document.querySelector('#graph-overlay svg') === window.drawn`) != true {
		t.Error("the graph was redrawn; the reader's view would reset")
	}
}
