//go:build browser && unix

package web

import "testing"

// Reading progress is document furniture: a graph pane has nothing to read
// through, so its head carries no "min read".
func TestBrowserProgressOnlyOnDocuments(t *testing.T) {
	srv := browserServer(t, paneScrollApp(t, graphFixture()))
	b := launchBrowser(t)
	b.navigate(t, srv.URL+"/t/w.io/d/a.md")
	if b.eval(t, `!!document.querySelector('.pane.focused .pane-progress')`) != true {
		t.Fatal("document pane shows no reading progress")
	}
	b.navigate(t, srv.URL+"/t/w.io/g/a.md")
	if b.eval(t, `!!document.querySelector('.pane-progress')`) != false {
		t.Fatal("graph pane shows reading progress")
	}
}
