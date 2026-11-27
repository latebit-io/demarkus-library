//go:build browser && unix

package web

import (
	"strings"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// A trail URL's #fragment names a section of the focused document (#123):
// only that pane scrolls to it, even when an earlier pane holds the same
// anchor, and the room itself stays put.
func TestBrowserFragmentScrollsOnlyTheFocusedPane(t *testing.T) {
	long := strings.Repeat("<p>"+strings.Repeat("word ", 120)+"</p>", 20)
	doc := func(name, lead string) domain.Document {
		return domain.Document{Title: name, Path: "/" + name + ".md",
			HTML: lead + long + `<h2 id="initiatives">Initiatives</h2>` + long}
	}
	svc := &fakeReading{docs: map[string]domain.Document{
		"/a.md": doc("a", ""), "/b.md": doc("b", ""), "/c.md": doc("c", ""),
		"/to.md": doc("to", `<p><a href="c.md#initiatives">to c</a></p>`),
	}}
	srv := browserServer(t, paneScrollApp(t, svc))
	b := launchBrowser(t)
	trail := func(world string) string {
		return "/t/" + world + "/d/a.md/~/" + world + "/d/b.md/~/" + world + "/d/c.md"
	}
	// Names each way the room moved besides the focused pane's section.
	const report = `(() => {
		const pane = document.querySelector('.pane.focused'), h = pane.querySelector('#initiatives');
		const off = h.getBoundingClientRect().top - pane.querySelector('.pane-head').getBoundingClientRect().bottom;
		const moved = [];
		if (document.scrollingElement.scrollTop !== 0) moved.push('page scrolled');
		if (document.querySelector('nav').getBoundingClientRect().top < 0) moved.push('nav hidden');
		if (off < 0 || off >= 40) moved.push('heading ' + Math.round(off) + 'px from the pane head');
		if ([...document.querySelectorAll('.pane:not(.focused)')].some(p => p.scrollTop !== 0)) moved.push('another pane scrolled');
		return moved.join(', ');
	})()`

	// A world each: fakeReading ignores it, but saved readings are keyed by
	// it, so one case's panes never carry into the next.
	cases := []struct {
		name  string
		reach func(t *testing.T)
	}{
		{"on load", func(t *testing.T) { b.navigate(t, srv.URL+trail("load.io")+"#initiatives") }},
		{"on a boosted link", func(t *testing.T) {
			b.navigate(t, srv.URL+"/t/link.io/d/a.md/~/link.io/d/b.md/~/link.io/d/to.md")
			// htmx scrolls to its anchor after the swap, just before finally:swap.
			b.eval(t, `addEventListener("htmx:finally:swap", () => window.__swapped = true);
				document.querySelector('.pane.focused .doc-body a[href^="/t/"]').click()`)
			b.waitFor(t, `window.__swapped && location.hash === "#initiatives"`)
		}},
		{"on hashchange", func(t *testing.T) {
			b.navigate(t, srv.URL+trail("hash.io"))
			b.eval(t, `location.hash = "initiatives"`)
			b.waitFor(t, `document.querySelector('.pane.focused').scrollTop > 0`)
		}},
		{"on Enter in the address bar", func(t *testing.T) {
			url := srv.URL + trail("enter.io") + "#initiatives"
			b.navigate(t, url)
			// The same URL again is a fragment navigation with no hash change.
			b.eval(t, `window.__from = navigation.currentEntry.key`)
			b.call(t, "Page.navigate", map[string]any{"url": url})
			b.waitFor(t, `navigation.currentEntry.key !== window.__from`)
		}},
		{"on reload", func(t *testing.T) {
			b.navigate(t, srv.URL+trail("reload.io")+"#initiatives")
			// The same URL too, but a new document: the reload must not be swallowed.
			b.eval(t, `window.__stale = true; location.reload()`)
			b.waitFor(t, `!window.__stale && document.readyState === "complete" && window.htmx`)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.reach(t)
			if moved := b.eval(t, report); moved != "" {
				t.Errorf("room moved: %v", moved)
			}
		})
	}
}

// An empty fragment ("#") names no section: the browser keeps its own jump to
// the top of the document. The classic room, where the page itself scrolls.
func TestBrowserEmptyFragmentReturnsToTheTop(t *testing.T) {
	long := strings.Repeat("<p>"+strings.Repeat("word ", 120)+"</p>", 20)
	svc := &fakeReading{docs: map[string]domain.Document{
		"/a.md": {Title: "a", Path: "/a.md", HTML: long + `<h2 id="initiatives">Initiatives</h2>` + long},
	}}
	srv := browserServer(t, readingApp(t, svc))
	b := launchBrowser(t)
	b.navigate(t, srv.URL+"/t/w.io/d/a.md#initiatives")
	b.waitFor(t, `document.scrollingElement.scrollTop > 0`)
	b.eval(t, `location.hash = ""`)
	b.waitFor(t, `location.href.endsWith("#") && document.scrollingElement.scrollTop === 0`)
}
