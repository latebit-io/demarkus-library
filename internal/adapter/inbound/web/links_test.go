package web

import (
	"reflect"
	"strings"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

func TestRewriteHref(t *testing.T) {
	const world = "soul" // current world for all cases
	cases := []struct {
		name, href, base, want string
	}{
		{"absolute doc", "/plans/universe-library.md", "/index.md", "/w/soul/d/plans/universe-library.md"},
		{"relative doc from root", "architecture.md", "/index.md", "/w/soul/d/architecture.md"},
		{"relative dir from root", "demarkus/", "/index.md", "/w/soul/d/demarkus/"},
		{"relative doc from subdir", "world.go", "/adapter/outbound/index.md", "/w/soul/d/adapter/outbound/world.go"},
		{"parent relative", "../index.md", "/a/b.md", "/w/soul/d/index.md"},
		{"encoded version link", "%2Fdemarkus-library%2Findex.md/v5", "/demarkus-library/index.md", "/w/soul/d/demarkus-library/index.md/v5"},
		{"external https left alone", "https://example.com/x", "/index.md", "https://example.com/x"},
		{"mailto left alone", "mailto:a@b.com", "/index.md", "mailto:a@b.com"},
		{"in-page anchor left alone", "#section", "/index.md", "#section"},
		{"fragment preserved", "architecture.md#design", "/index.md", "/w/soul/d/architecture.md#design"},

		// Cross-world traversal — the distributed knowledge graph. The
		// mark:// authority is the target world: a knowledge-system name
		// or a host[:port] reached directly.
		{"mark same world", "mark://soul/architecture.md", "/index.md", "/w/soul/d/architecture.md"},
		{"mark cross world name", "mark://team-a/index.md", "/index.md", "/w/team-a/d/index.md"},
		{"mark cross world host", "mark://wiki.example.org/notes.md", "/index.md", "/w/wiki.example.org/d/notes.md"},
		{"mark cross world host port", "mark://wiki.example.org:6310/notes.md", "/index.md", "/w/wiki.example.org:6310/d/notes.md"},
		{"mark world root", "mark://team-a/", "/index.md", "/w/team-a/d/"},
		{"mark world no path", "mark://team-a", "/index.md", "/w/team-a/d/"},
	}
	for _, tc := range cases {
		if got, _, _ := rewriteHref(tc.href, world, tc.base); got != tc.want {
			t.Errorf("%s: rewriteHref(%q, %q, %q) = %q, want %q", tc.name, tc.href, world, tc.base, got, tc.want)
		}
	}
}

func TestLinkifyCatalogPaths(t *testing.T) {
	in := `<table><tbody>` +
		`<tr><td>/plans/universe-library.md</td><td>0.70</td><td>Universe Library</td></tr>` +
		`</tbody></table>`
	out := linkifyCatalogPaths(in, "soul")
	if !strings.Contains(out, `<td><a href="/w/soul/d/plans/universe-library.md">/plans/universe-library.md</a></td>`) {
		t.Errorf("path cell not linkified: %s", out)
	}
	if !strings.Contains(out, `<td>0.70</td>`) {
		t.Errorf("non-path cell should be untouched: %s", out)
	}
	if strings.Contains(out, `d/Universe`) {
		t.Errorf("title cell should not be linkified: %s", out)
	}
}

func TestRewriteLinksInFragment(t *testing.T) {
	in := `<p>see <a href="architecture.md">arch</a>, <a href="mark://team-a/x.md">far</a>, and <a href="https://x.com">ext</a></p>`
	out, _ := rewriteLinks(in, "soul", "/index.md")
	if !strings.Contains(out, `href="/w/soul/d/architecture.md"`) {
		t.Errorf("internal link not rewritten: %s", out)
	}
	if !strings.Contains(out, `href="/w/team-a/d/x.md"`) {
		t.Errorf("cross-world link not rewritten: %s", out)
	}
	if !strings.Contains(out, `href="https://x.com"`) {
		t.Errorf("external link should be untouched: %s", out)
	}
}

// rewriteLinks also returns the observed document edges (R3): document
// targets only — external links, in-page anchors, and listings (dirs) are
// not edges, and duplicates collapse.
func TestRewriteLinksReturnsDocumentEdges(t *testing.T) {
	in := `<p>` +
		`<a href="architecture.md">a</a> ` +
		`<a href="architecture.md">a again</a> ` + // duplicate → collapses
		`<a href="mark://team-a/x.md">cross</a> ` +
		`<a href="plans/">dir</a> ` + // listing → not an edge
		`<a href="#sec">anchor</a> ` + // anchor → not an edge
		`<a href="https://x.com">ext</a>` + // external → not an edge
		`</p>`
	_, edges := rewriteLinks(in, "soul", "/index.md")
	want := []domain.Ref{
		{World: "soul", Path: "/architecture.md"},
		{World: "team-a", Path: "/x.md"},
	}
	if !reflect.DeepEqual(edges, want) {
		t.Errorf("edges = %v, want %v", edges, want)
	}
}

func TestRewriteHrefReportsDocVsListing(t *testing.T) {
	if _, ref, isDoc := rewriteHref("architecture.md", "soul", "/index.md"); !isDoc ||
		ref != (domain.Ref{World: "soul", Path: "/architecture.md"}) {
		t.Errorf("doc link: isDoc=%v ref=%v", isDoc, ref)
	}
	if _, _, isDoc := rewriteHref("plans/", "soul", "/index.md"); isDoc {
		t.Errorf("listing should not be a document edge")
	}
	if _, _, isDoc := rewriteHref("https://x.com", "soul", "/index.md"); isDoc {
		t.Errorf("external should not be a document edge")
	}
}
