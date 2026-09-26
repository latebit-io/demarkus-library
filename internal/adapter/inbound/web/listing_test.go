package web

import (
	"strings"
	"testing"
)

func TestListingFragmentShelvesFoldersFirst(t *testing.T) {
	frag := `<ul>` +
		`<li><a href="/w/team-a/d/adr/">adr/</a></li>` +
		`<li><a href="/w/team-a/d/b.md">b.md</a></li>` +
		`<li><a href="/w/team-a/d/c/">c/</a></li>` +
		`<li><a href="/w/team-a/d/a.md">a.md</a></li>` +
		`</ul>`
	out := listingFragment(frag)

	if !strings.HasPrefix(out, `<div class="listing"><ul>`) {
		t.Fatalf("listing should be wrapped: %s", out)
	}
	// Folders lead; each group keeps the world's order.
	order := []string{">adr/<", ">c/<", ">b.md<", ">a.md<"}
	last := -1
	for _, want := range order {
		i := strings.Index(out, want)
		if i <= last {
			t.Fatalf("want order %v, got %s", order, out)
		}
		last = i
	}
}
