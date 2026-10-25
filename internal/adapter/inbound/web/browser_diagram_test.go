//go:build browser && unix

package web

import (
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// A rendered mermaid diagram pops out into the overlay with the shared
// camera, and parks back home at fit when the overlay closes.
func TestBrowserDiagramPopout(t *testing.T) {
	svc := &fakeReading{
		docs: map[string]domain.Document{
			"/d.md": {Title: "Flow", Path: "/d.md",
				HTML: "<pre><code class=\"language-mermaid\">graph TD; A-->B; B-->C;</code></pre>"},
		},
	}
	srv := browserServer(t, readingApp(t, svc))
	b := launchBrowser(t)
	b.navigate(t, srv.URL+"/t/w.io/d/d.md")
	b.waitFor(t, `document.querySelector('pre.mermaid > svg.diagram') && document.querySelector('pre.mermaid > button.diagram-pop')`)

	b.eval(t, `window.keptSVG = document.querySelector('pre.mermaid svg'); window.keptBox = keptSVG.getAttribute('viewBox');
		document.querySelector('button.diagram-pop').click()`)
	b.waitFor(t, `!document.querySelector('#diagram-overlay').hidden && document.querySelector('#diagram-overlay .graph-canvas svg') === keptSVG`)
	if b.eval(t, `document.querySelector('main').inert &&
		document.querySelector('#diagram-overlay .graph-title').textContent === 'Flow' &&
		document.activeElement === document.querySelector('#diagram-overlay .graph-panel')`) != true {
		t.Fatal("opening did not take the room, name the document, and focus the dialog")
	}
	b.key(t, "+")
	b.waitFor(t, `document.querySelector('#diagram-overlay .graph-fit').textContent === '143%'`)
	if b.eval(t, `keptSVG.getAttribute('viewBox') !== keptBox`) != true {
		t.Fatal("zooming did not move the diagram camera")
	}
	b.key(t, "Escape")
	b.waitFor(t, `document.querySelector('#diagram-overlay').hidden`)
	if b.eval(t, `document.querySelector('pre.mermaid > svg') === keptSVG && keptSVG.getAttribute('viewBox') === keptBox &&
		!document.querySelector('main').inert &&
		document.activeElement === document.querySelector('button.diagram-pop')`) != true {
		t.Fatal("closing did not park the diagram home at fit and restore focus")
	}
}
