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
