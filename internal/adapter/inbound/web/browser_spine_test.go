//go:build browser && unix

package web

import (
	"fmt"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// A document left half read and then collapsed keeps that reading on its
// spine: the thumb index fills to it and the peek marks the section.
func TestBrowserSpineShowsSavedReading(t *testing.T) {
	long := strings.Repeat("<p>"+strings.Repeat("word ", 120)+"</p>", 30)
	svc := &fakeReading{docs: map[string]domain.Document{
		"/a.md": {Title: "A", Path: "/a.md",
			HTML: `<p><a href="b.md">next</a></p><h2 id="early">Early</h2>` + long + `<h2 id="late">Late</h2><p>end</p>`},
		"/b.md": {Title: "B", Path: "/b.md", HTML: `<p><a href="c.md">next</a></p>`},
		"/c.md": {Title: "C", Path: "/c.md", HTML: "<p>c</p>"},
	}}
	srv := browserServer(t, paneScrollApp(t, svc))
	b := launchBrowser(t)
	b.navigate(t, srv.URL+"/t/w.io/d/a.md")
	b.eval(t, `(() => { const pane = document.querySelector('.pane.focused');
		pane.scrollTop = (pane.scrollHeight - pane.clientHeight) / 2; })()`)
	for _, next := range []string{"/b.md", "/c.md"} {
		b.eval(t, `document.querySelector('.pane.focused .doc-body a[href^="/t/"]').click()`)
		b.waitFor(t, fmt.Sprintf("location.pathname.endsWith(%q)", next))
	}

	const spine = `document.querySelector('.pane.spine[data-key="w.io/a.md"]')`
	b.waitFor(t, spine+` && `+spine+`.style.getPropertyValue('--read') !== ''`)
	// The pane reflows as it steps from focused to body, so the percent is
	// whatever its head last showed: near half, not exactly.
	got, _ := b.eval(t, `(() => { const spine = `+spine+`;
		const read = spine.style.getPropertyValue('--read');
		return [parseInt(read, 10) > 25 && parseInt(read, 10) < 75,
			spine.querySelector('.spine-progress').textContent.startsWith(read + ' '),
			spine.querySelectorAll('.spine-tick.read').length,
			spine.querySelector('.peek-sections .here').textContent].join('|'); })()`).(string)
	if got != "true|true|1|Early" {
		t.Errorf("spine reading = %q, want near half read, matching the card, one tick passed, in Early", got)
	}
	if left, _ := b.eval(t, spine+`.querySelector('.spine-left').textContent`).(string); !strings.HasSuffix(left, " min left") {
		t.Errorf("spine time left = %q", left)
	}
}

// Right of the focus the room's edge is near, so a spine's peek opens leftward;
// left of it the peek opens rightward. CSS alone decides, from pane order.
func TestBrowserSpinePeekOpensTowardTheRoom(t *testing.T) {
	docs := map[string]domain.Document{}
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		docs["/"+name+".md"] = domain.Document{Title: name, Path: "/" + name + ".md", HTML: "<p>text</p>"}
	}
	srv := browserServer(t, paneScrollApp(t, &fakeReading{docs: docs}))
	b := launchBrowser(t)
	b.navigate(t, srv.URL+"/t/w.io/d/a.md/~/w.io/d/b.md/~/w.io/d/c.md/~/w.io/d/d.md/~/w.io/d/e.md?focus=2")
	got, _ := b.eval(t, `["w.io/a.md", "w.io/e.md"].map(key => {
		const spine = document.querySelector('.pane.spine[data-key="' + key + '"]');
		const card = spine.querySelector('.spine-peek').getBoundingClientRect();
		return card.left >= spine.getBoundingClientRect().right - 1 ? "right" : "left"; }).join()`).(string)
	if got != "right,left" {
		t.Errorf("peeks open %q, want the first rightward and the last leftward", got)
	}
}

// In the pane-scroll room the librarian fills its pane: the ask box sits at
// the pane's foot however short the conversation is.
func TestBrowserLibrarianAskBoxSitsAtPaneFoot(t *testing.T) {
	lib := &fakeLibrarian{history: []domain.LibrarianExchange{{Question: "q", Answer: "short"}}}
	app := echo.New()
	view, err := NewView()
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}
	app.Renderer = view
	RoomRoutes(app, librarianRoom(lib).WithPaneScroll())
	b := launchBrowser(t)
	b.navigate(t, browserServer(t, app).URL+"/t/u/~/a")
	gap, _ := b.eval(t, `(() => { const pane = document.querySelector('.pane[data-kind="a"]');
		return Math.round(pane.getBoundingClientRect().bottom - pane.querySelector('.ask-row').getBoundingClientRect().bottom); })()`).(float64)
	if gap < 0 || gap > 24 {
		t.Errorf("ask box sits %vpx above the pane's foot, want it at the foot", gap)
	}
}
