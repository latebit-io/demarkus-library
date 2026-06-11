package web

import (
	"strings"
	"testing"
)

func TestRewriteHref(t *testing.T) {
	cases := []struct {
		name, href, base, want string
	}{
		{"absolute doc", "/plans/universe-library.md", "/index.md", "/d/plans/universe-library.md"},
		{"relative doc from root", "architecture.md", "/index.md", "/d/architecture.md"},
		{"relative dir from root", "demarkus/", "/index.md", "/d/demarkus/"},
		{"relative doc from subdir", "world.go", "/adapter/outbound/index.md", "/d/adapter/outbound/world.go"},
		{"parent relative", "../index.md", "/a/b.md", "/d/index.md"},
		{"mark url", "mark://soul.demarkus.io/architecture.md", "/index.md", "/d/architecture.md"},
		{"encoded version link", "%2Fdemarkus-library%2Findex.md/v5", "/demarkus-library/index.md", "/d/demarkus-library/index.md/v5"},
		{"external https left alone", "https://example.com/x", "/index.md", "https://example.com/x"},
		{"mailto left alone", "mailto:a@b.com", "/index.md", "mailto:a@b.com"},
		{"in-page anchor left alone", "#section", "/index.md", "#section"},
		{"fragment preserved", "architecture.md#design", "/index.md", "/d/architecture.md#design"},
	}
	for _, tc := range cases {
		if got := rewriteHref(tc.href, tc.base); got != tc.want {
			t.Errorf("%s: rewriteHref(%q, %q) = %q, want %q", tc.name, tc.href, tc.base, got, tc.want)
		}
	}
}

func TestLinkifyCatalogPaths(t *testing.T) {
	in := `<table><tbody>` +
		`<tr><td>/plans/universe-library.md</td><td>0.70</td><td>Universe Library</td></tr>` +
		`</tbody></table>`
	out := linkifyCatalogPaths(in)
	if !strings.Contains(out, `<td><a href="/d/plans/universe-library.md">/plans/universe-library.md</a></td>`) {
		t.Errorf("path cell not linkified: %s", out)
	}
	if !strings.Contains(out, `<td>0.70</td>`) {
		t.Errorf("non-path cell should be untouched: %s", out)
	}
	if strings.Contains(out, `<a href="/d/Universe`) {
		t.Errorf("title cell should not be linkified: %s", out)
	}
}

func TestRewriteLinksInFragment(t *testing.T) {
	in := `<p>see <a href="architecture.md">arch</a> and <a href="https://x.com">ext</a></p>`
	out := rewriteLinks(in, "/index.md")
	if !strings.Contains(out, `href="/d/architecture.md"`) {
		t.Errorf("internal link not rewritten: %s", out)
	}
	if !strings.Contains(out, `href="https://x.com"`) {
		t.Errorf("external link should be untouched: %s", out)
	}
}
