package web

import (
	"strings"
	"testing"
)

func doc(world, path string) paneAddr { return paneAddr{Kind: paneDoc, World: world, Value: path} }
func tag(world, t string) paneAddr    { return paneAddr{Kind: paneTag, World: world, Value: t} }

func TestTrailRoundTrip(t *testing.T) {
	cases := []struct {
		name  string
		trail trail
		url   string
	}{
		{"single doc", trail{Panes: []paneAddr{doc("soul.demarkus.io", "/index.md")}, Focus: 0},
			"/t/soul.demarkus.io/d/index.md"},
		{"doc with nested path", trail{Panes: []paneAddr{doc("root", "/adr/0005-x.md")}, Focus: 0},
			"/t/root/d/adr/0005-x.md"},
		{"listing pane (trailing slash)", trail{Panes: []paneAddr{doc("root", "/plans/")}, Focus: 0},
			"/t/root/d/plans/"},
		{"world-root listing (path /)", trail{Panes: []paneAddr{doc("world-a", "/")}, Focus: 0},
			"/t/world-a/d/"},
		{"multi pane mixed kinds", trail{Panes: []paneAddr{
			doc("soul.demarkus.io", "/index.md"), tag("root", "adr"), doc("root", "/adr/1.md")}, Focus: 2},
			"/t/soul.demarkus.io/d/index.md/~/root/tags/adr/~/root/d/adr/1.md"},
		{"non-default focus", trail{Panes: []paneAddr{
			doc("a.io", "/x.md"), doc("a.io", "/y.md")}, Focus: 0},
			"/t/a.io/d/x.md/~/a.io/d/y.md?focus=0"},
		{"host world with port", trail{Panes: []paneAddr{doc("w.example.org:6310", "/x.md")}, Focus: 0},
			"/t/w.example.org:6310/d/x.md"},
	}
	for _, tc := range cases {
		if got := trailURL(tc.trail); got != tc.url {
			t.Errorf("%s: trailURL = %q, want %q", tc.name, got, tc.url)
		}
		rest, focus, _ := strings.Cut(strings.TrimPrefix(tc.url, "/t/"), "?focus=")
		parsed, err := parseTrail(rest, focus)
		if err != nil {
			t.Fatalf("%s: parseTrail(%q): %v", tc.name, rest, err)
		}
		if len(parsed.Panes) != len(tc.trail.Panes) || parsed.Focus != tc.trail.Focus {
			t.Fatalf("%s: parsed %+v, want %+v", tc.name, parsed, tc.trail)
		}
		for i := range parsed.Panes {
			if parsed.Panes[i] != tc.trail.Panes[i] {
				t.Errorf("%s: pane %d = %+v, want %+v", tc.name, i, parsed.Panes[i], tc.trail.Panes[i])
			}
		}
	}
}

func TestParseTrailFloorToWorldRootListing(t *testing.T) {
	// Regression: the floor's world node links to the world-root listing,
	// producing the chunk "<world>/d/" (path "/"). This whole trail must
	// parse — an empty doc value is a root listing, not malformed.
	tr, err := parseTrail("u/~/world-a/d/", "")
	if err != nil {
		t.Fatalf("parseTrail(floor→world-root): %v", err)
	}
	if len(tr.Panes) != 2 {
		t.Fatalf("panes = %d, want 2", len(tr.Panes))
	}
	if tr.Panes[0].Kind != paneFloor {
		t.Errorf("pane 0 = %+v, want floor", tr.Panes[0])
	}
	if tr.Panes[1] != (paneAddr{Kind: paneDoc, World: "world-a", Value: "/"}) {
		t.Errorf("pane 1 = %+v, want world-a root listing", tr.Panes[1])
	}
}

func TestPaneAddrFromRouteWorldRoot(t *testing.T) {
	// The trailize pass (/w/ route decode) must agree with parsePaneChunk
	// (/t/ chunk parse) on the world-root listing, or a doc linking to a
	// world root renders an un-trailized link.
	got, frag, ok := paneAddrFromRoute("/w/world-a/d/")
	if !ok || frag != "" {
		t.Fatalf("paneAddrFromRoute(/w/world-a/d/) = (_, %q, %v)", frag, ok)
	}
	want := paneAddr{Kind: paneDoc, World: "world-a", Value: "/"}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	// A subdir route still decodes (regression guard for the non-empty case).
	if a, _, ok := paneAddrFromRoute("/w/world-a/d/plans/"); !ok || a.Value != "/plans/" {
		t.Errorf("subdir route = %+v ok=%v", a, ok)
	}
	// An empty tag route is still rejected.
	if _, _, ok := paneAddrFromRoute("/w/world-a/tags/"); ok {
		t.Errorf("empty tag route should be rejected")
	}
}

func TestTagSlashRejectedAfterUnescape(t *testing.T) {
	// A raw "foo%2Fbar" has no literal slash but unescapes to "foo/bar" —
	// it must be rejected as a multi-segment tag by BOTH decoders, not
	// silently accepted because the check ran before unescaping.
	if _, err := parseTrail("root/tags/foo%2Fbar", ""); err == nil {
		t.Errorf("chunk parser accepted escaped-slash tag")
	}
	if _, _, ok := paneAddrFromRoute("/w/root/tags/foo%2Fbar"); ok {
		t.Errorf("route decoder accepted escaped-slash tag")
	}
	// A legitimately escaped tag (no slash) still decodes — e.g. the axis
	// colon in "category:reference".
	a, err := parseTrail("root/tags/category%3Areference", "")
	if err != nil || a.Panes[0] != (paneAddr{Kind: paneTag, World: "root", Value: "category:reference"}) {
		t.Errorf("escaped non-slash tag = %+v, err=%v", a.Panes, err)
	}
}

func TestParseTrailRejectsMalformed(t *testing.T) {
	for _, rest := range []string{
		"",                   // no panes
		"soul",               // no kind
		"soul/d",             // no path
		"soul/versions/x.md", // unknown pane kind
		"soul/tags/a/b",      // tag with a slash
		"/d/x.md",            // empty world
		"~/d/x.md",           // separator as world
		"soul/d/x.md/~/",     // trailing separator → empty chunk
		"soul/d/x.md/~/soul", // second chunk malformed
	} {
		if _, err := parseTrail(rest, ""); err == nil {
			t.Errorf("parseTrail(%q) accepted", rest)
		}
	}
}

func TestParseTrailCapAndFocusClamp(t *testing.T) {
	chunks := make([]string, 0, maxPanes+1)
	for range maxPanes + 1 {
		chunks = append(chunks, "w.io/d/x.md")
	}
	if _, err := parseTrail(strings.Join(chunks, "/~/"), ""); err == nil {
		t.Error("over-cap trail accepted")
	}

	tr, err := parseTrail("a.io/d/x.md/~/a.io/d/y.md", "99")
	if err != nil {
		t.Fatalf("parseTrail: %v", err)
	}
	if tr.Focus != 1 {
		t.Errorf("focus = %d, want clamped to 1", tr.Focus)
	}
	if tr, _ = parseTrail("a.io/d/x.md", "-3"); tr.Focus != 0 {
		t.Errorf("negative focus = %d, want 0", tr.Focus)
	}
	if tr, _ = parseTrail("a.io/d/x.md/~/a.io/d/y.md", "junk"); tr.Focus != 1 {
		t.Errorf("junk focus = %d, want default last", tr.Focus)
	}
}

func TestTrailAfterClickTruncatesAndAppends(t *testing.T) {
	start := trail{Panes: []paneAddr{doc("w", "/a.md"), doc("w", "/b.md"), doc("w", "/c.md")}, Focus: 2}

	// Click in pane 1 (B): C is off the path and does not survive.
	got := trailAfterClick(start, 1, doc("w", "/d.md"))
	want := []paneAddr{doc("w", "/a.md"), doc("w", "/b.md"), doc("w", "/d.md")}
	if len(got.Panes) != 3 || got.Focus != 2 {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got.Panes[i] != want[i] {
			t.Errorf("pane %d = %+v, want %+v", i, got.Panes[i], want[i])
		}
	}
}

func TestTrailAfterClickDedupsToFocus(t *testing.T) {
	start := trail{Panes: []paneAddr{doc("w", "/a.md"), doc("w", "/b.md"), doc("w", "/c.md")}, Focus: 2}
	// C links back to A: no duplicate, no truncation — focus jumps.
	got := trailAfterClick(start, 2, doc("w", "/a.md"))
	if len(got.Panes) != 3 || got.Focus != 0 {
		t.Errorf("circular reference mishandled: %+v", got)
	}
}

func TestTrailAfterClickDropsOldestPastCap(t *testing.T) {
	full := trail{Focus: maxPanes - 1}
	for i := range maxPanes {
		full.Panes = append(full.Panes, doc("w", "/p"+strings.Repeat("x", i)+".md"))
	}
	got := trailAfterClick(full, maxPanes-1, doc("w", "/new.md"))
	if len(got.Panes) != maxPanes {
		t.Fatalf("len = %d, want %d", len(got.Panes), maxPanes)
	}
	if got.Panes[0] == full.Panes[0] {
		t.Error("oldest pane should have been dropped")
	}
	if got.Panes[maxPanes-1] != doc("w", "/new.md") || got.Focus != maxPanes-1 {
		t.Errorf("append/focus wrong: %+v", got)
	}
}

func TestTrailFocusedMovesAttentionOnly(t *testing.T) {
	start := trail{Panes: []paneAddr{doc("w", "/a.md"), doc("w", "/b.md")}, Focus: 1}
	got := trailFocused(start, 0)
	if got.Focus != 0 || len(got.Panes) != 2 {
		t.Errorf("spine click must not change the path: %+v", got)
	}
	if trailURL(got) != "/t/w/d/a.md/~/w/d/b.md?focus=0" {
		t.Errorf("url = %q", trailURL(got))
	}
}
