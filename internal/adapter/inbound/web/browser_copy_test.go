//go:build browser && unix

package web

import (
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// The copy affordance (#94): `c` and the pane-head icon copy the pane's
// address, a same-pane #fragment rides along, a heading's clone copies its
// anchor. navigator.clipboard is stubbed: headless Chrome exposes none.
func TestBrowserCopyMarkAddress(t *testing.T) {
	svc := &fakeReading{
		docs: map[string]domain.Document{
			"/a.md": {Title: "A", Path: "/a.md", HTML: `<h2 id="sec">Sec</h2><p>a</p>`},
		},
	}
	srv := browserServer(t, svc)
	b := launchBrowser(t)
	b.navigate(t, srv.URL+"/t/w.io/d/a.md")
	b.eval(t, `window.copied = [];
		navigator.clipboard.writeText = function (s) { copied.push(s); return Promise.resolve(); }`)
	b.waitFor(t, `!document.querySelector('.pane.focused .pane-head button.copy-mark').hidden`)
	b.key(t, "c")
	b.waitFor(t, `copied.length === 1 && copied[0] === 'mark://w.io/a.md'`)
	b.waitFor(t, `document.querySelector('.pane.focused .pane-head button.copy-mark').classList.contains('copied')`)

	b.eval(t, `location.hash = '#sec'`)
	x, y := b.centre(t, ".pane.focused .pane-head button.copy-mark")
	b.click(t, x, y)
	b.waitFor(t, `copied.length === 2 && copied[1] === 'mark://w.io/a.md#sec'`)

	// A fragment that lives in no pane is not this document's section.
	b.eval(t, `location.hash = '#elsewhere'`)
	b.key(t, "c")
	b.waitFor(t, `copied.length === 3 && copied[2] === 'mark://w.io/a.md'`)

	// The heading's own icon copies the anchor regardless of the URL.
	b.eval(t, `document.querySelector('.pane.focused h2#sec .copy-anchor').click()`)
	b.waitFor(t, `copied.length === 4 && copied[3] === 'mark://w.io/a.md#sec'`)
}
