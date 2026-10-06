//go:build browser && unix

package web

import (
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

func graphBrowser(t *testing.T) *cdp {
	t.Helper()
	svc := &fakeReading{
		docs: map[string]domain.Document{
			"/a.md": {Title: "A", Path: "/a.md", HTML: "<p>a</p>"},
			"/b.md": {Title: "B", Path: "/b.md", HTML: "<p>b</p>"},
		},
		neighbor: map[string]domain.Neighborhood{
			"/a.md": {Center: domain.Ref{World: "w.io", Path: "/a.md"},
				Out: []domain.Ref{{World: "w.io", Path: "/b.md"}, {World: "w.io", Path: "/c.md"}}},
			"/b.md": {Center: domain.Ref{World: "w.io", Path: "/b.md"},
				In: []domain.Ref{{World: "w.io", Path: "/a.md"}}},
		},
		worldMap: testWorldMap(),
	}
	srv := browserServer(t, svc)
	b := launchBrowser(t)
	b.navigate(t, srv.URL+"/t/w.io/d/a.md")
	return b
}

func TestBrowserGraphWorkspaceLifecycle(t *testing.T) {
	b := graphBrowser(t)
	b.eval(t, `document.querySelector('.nav-key.graph-open').focus()`)
	b.key(t, "g")
	if b.eval(t, `(() => {
		const r = document.querySelector('#graph-overlay .graph-panel').getBoundingClientRect();
		return r.left === 0 && r.top === 0 && r.right <= innerWidth && r.bottom <= innerHeight;
	})()`) != true {
		t.Fatal("graph workspace does not fit the viewport")
	}
	b.key(t, "Tab")
	if b.eval(t, `document.activeElement.matches('#graph-overlay .map-filter')`) != true {
		t.Fatal("Tab did not enter the graph controls")
	}
	b.eval(t, `document.querySelector('#graph-overlay svg a:last-of-type').focus()`)
	b.key(t, "Tab")
	if b.eval(t, `document.activeElement.matches('#graph-overlay .map-filter')`) != true {
		t.Fatal("Tab escaped the graph workspace")
	}
	b.key(t, "Escape")
	if b.eval(t, `document.activeElement.matches('.nav-key.graph-open') && !document.querySelector('main').inert`) != true {
		t.Fatal("closing did not restore focus and release the reading room")
	}
	b.key(t, "g")
	b.key(t, "m")
	b.waitFor(t, `document.querySelector('#map-canvas svg') && !document.querySelector('#map-overlay')._loading`)
	if b.eval(t, `document.querySelector('#graph-overlay').hidden && document.querySelector('main').inert`) != true {
		t.Fatal("switching graphs left two workspaces active")
	}
	b.key(t, "+")
	b.waitFor(t, `document.querySelector('#map-overlay .graph-fit').textContent === '143%'`)
	b.eval(t, `window.keptMap = document.querySelector('#map-canvas svg');
		window.keptBox = keptMap.getAttribute('viewBox');
		const input = document.querySelector('#map-overlay .map-filter');
		input.value = 'hub'; input.dispatchEvent(new Event('input', {bubbles:true}));
		document.querySelector('#map-overlay [data-graph-action="close"]').click()`)
	b.key(t, "m")
	if b.eval(t, `document.querySelector('#map-canvas svg') === keptMap &&
		keptMap.getAttribute('viewBox') === keptBox &&
		document.querySelector('#map-overlay .map-filter').value === 'hub'`) != true {
		t.Fatal("reopening discarded the map, its camera or filter")
	}
	b.eval(t, `document.querySelector('#map-overlay .map-filter').focus()`)
	b.key(t, "Escape")
	if b.eval(t, `!document.querySelector('#map-overlay').hidden && !document.querySelector('#map-overlay .map-filter').value`) != true {
		t.Fatal("filter Escape should clear the query before closing")
	}
	b.key(t, "Escape")
	if b.eval(t, `document.activeElement.matches('.nav-key.graph-open')`) != true {
		t.Fatal("switching workspaces lost the original return focus")
	}
}

func TestBrowserGraphCameraAndExploration(t *testing.T) {
	b := graphBrowser(t)
	b.key(t, "g")
	b.eval(t, `window.graph = document.querySelector('#graph-overlay svg');
		window.baseBox = graph.getAttribute('viewBox');
		window.anchor = new DOMPoint(420, 320).matrixTransform(graph.getScreenCTM().inverse())`)
	b.call(t, "Input.dispatchMouseEvent", map[string]any{
		"type": "mouseWheel", "x": 420, "y": 320, "deltaX": 0, "deltaY": -100,
	})
	b.waitFor(t, `document.querySelector('#graph-overlay .graph-fit').textContent === '118%'`)
	if b.eval(t, `(() => {
		const point = anchor.matrixTransform(graph.getScreenCTM());
		return Math.hypot(point.x - 420, point.y - 320) < 0.5;
	})()`) != true {
		t.Fatal("wheel zoom moved the point under the cursor")
	}
	// A drag starting on a real document link must pan rather than navigate.
	x, y := b.centre(t, `#graph-overlay a[data-node="/b.md"] circle`)
	b.call(t, "Input.dispatchMouseEvent", map[string]any{
		"type": "mousePressed", "x": x, "y": y, "button": "left", "buttons": 1, "clickCount": 1,
	})
	b.call(t, "Input.dispatchMouseEvent", map[string]any{
		"type": "mouseMoved", "x": x + 70, "y": y + 35, "button": "left", "buttons": 1,
	})
	b.call(t, "Input.dispatchMouseEvent", map[string]any{
		"type": "mouseReleased", "x": x + 70, "y": y + 35, "button": "left", "clickCount": 1,
	})
	b.key(t, "0")
	b.waitFor(t, `graph.getAttribute('viewBox') === baseBox`)
	if b.eval(t, `location.pathname === '/t/w.io/d/a.md'`) != true {
		t.Fatal("dragging a document followed its link")
	}
	b.key(t, "+")
	b.waitFor(t, `document.querySelector('#graph-overlay .graph-fit').textContent === '143%'`)
	b.eval(t, `window.beforeExplore = graph.getAttribute('viewBox');
		document.querySelector('#graph-overlay [data-graph-action="explore"]').click()`)
	x, y = b.centre(t, `#graph-overlay a[data-node="/b.md"] circle`)
	b.click(t, x, y)
	b.waitFor(t, `document.querySelector('#graph-overlay .graph-title').textContent === 'b' &&
		!document.querySelector('#graph-overlay')._loading`)
	if b.eval(t, `location.pathname === '/t/w.io/d/a.md' && document.querySelectorAll('.graph-crumb').length === 1`) != true {
		t.Fatal("exploration should keep the reading trail and add graph history")
	}
	b.eval(t, `document.querySelector('.graph-crumb').click()`)
	b.waitFor(t, `document.querySelector('#graph-overlay svg').getAttribute('viewBox') === beforeExplore`)
	b.key(t, "0")
	b.waitFor(t, `document.querySelector('#graph-overlay svg').getAttribute('viewBox') === baseBox`)
}
