package web

import (
	"strings"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

func TestIndexifyEnrichesDocRows(t *testing.T) {
	// A rendered listing (post-rewriteLinks): a document file + a subdirectory.
	frag := `<ul>` +
		`<li><a href="/w/team-a/d/plans/mission.md">mission.md</a></li>` +
		`<li><a href="/w/team-a/d/plans/sub/">sub/</a></li>` +
		`</ul>`
	entries := map[string]domain.IndexEntry{
		"/plans/mission.md": {Title: "The Mission", Path: "/plans/mission.md", World: "team-a", Status: "accepted", Orphan: true},
		"/plans/b.md":       {Title: "B", Path: "/plans/b.md", World: "team-a", Status: "draft"},
		"/plans/c.md":       {Title: "C", Path: "/plans/c.md", World: "team-a", Status: "draft"},
	}
	out := indexify(frag, entries)

	if !strings.Contains(out, `>The Mission</a>`) {
		t.Errorf("row should lead with the title: %s", out)
	}
	if !strings.Contains(out, `class="idx-file">mission.md`) {
		t.Errorf("filename should be mono secondary: %s", out)
	}
	if !strings.Contains(out, `status status-accepted`) {
		t.Errorf("status badge missing: %s", out)
	}
	if !strings.Contains(out, `class="idx-orphan"`) {
		t.Errorf("orphan tag missing: %s", out)
	}
	// Subdirectory rows are left untouched (not documents).
	if !strings.Contains(out, `>sub/</a>`) {
		t.Errorf("subdirectory row should be untouched: %s", out)
	}
}

// Rows at the world's most common status go unmarked, and the listing says
// once what an unmarked row means; the exception keeps its badge.
func TestIndexifyQuietsCommonStatus(t *testing.T) {
	frag := `<ul>` +
		`<li><a href="/w/w/d/a.md">a.md</a></li>` +
		`<li><a href="/w/w/d/b.md">b.md</a></li>` +
		`<li><a href="/w/w/d/c.md">c.md</a></li>` +
		`</ul>`
	entries := map[string]domain.IndexEntry{
		"/a.md": {Title: "A", Path: "/a.md", Status: "draft"},
		"/b.md": {Title: "B", Path: "/b.md", Status: "draft"},
		"/c.md": {Title: "C", Path: "/c.md", Status: "wip"},
	}
	out := indexify(frag, entries)
	if strings.Contains(out, "status-draft") {
		t.Errorf("the common status should carry no badge: %s", out)
	}
	if !strings.Contains(out, `status status-wip`) {
		t.Errorf("an uncommon status keeps its badge: %s", out)
	}
	if !strings.HasSuffix(out, `<p class="idx-note">Unmarked documents are draft.</p>`) {
		t.Errorf("the listing should explain unmarked rows: %s", out)
	}
}
