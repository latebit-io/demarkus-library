package web

import (
	"regexp"
	"strings"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// The shown edition links out too, so its preview anchors differ per render.
var (
	tailV1 = domain.Document{Title: "X", Path: "/x.md", Version: "1", HTML: "<p>a <a href=\"z.md\">z</a></p>\n"}
	tailV2 = domain.Document{Title: "X", Path: "/x.md", Version: "2",
		HTML: tailV1.HTML + "<h2>Later</h2>\n<p>see <a href=\"y.md\">y</a></p>\n"}
)

// tailAdded is the markup a stream added to its pane; "" for none.
func tailAdded(body string) string {
	m := tailFrame.FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	return strings.ReplaceAll(m[1], "\ndata: ", "\n")
}

var tailFrame = regexp.MustCompile(`(?s)hx-target="#live-doc" hx-swap="beforeend">(.*?)</hx-partial>`)

// tailURL is the stream of a pane showing /x.md at edition shown, on the
// trail rest (its path past /t/).
func tailURL(t *testing.T, shown, rest string) string {
	t.Helper()
	return liveEditionURL(liveDoc{world: "soul.demarkus.io", path: "/x.md", shown: shown,
		tail: &liveTail{trail: testTrail(t, rest)}})
}

func TestLiveTail(t *testing.T) {
	t.Parallel()
	tailTarget := tailURL(t, "1", "soul.demarkus.io/d/x.md")
	rewritten := tailV2
	rewritten.HTML = "<p>A</p>\n<p>b</p>\n"
	retitled := tailV2
	retitled.Title = "Y"
	restatused := tailV2
	restatused.Status = "accepted"
	republished := tailV1
	republished.Version = "2"
	tests := []struct {
		name       string
		target     string
		results    []readResult
		pinned     map[string]readResult
		signals    int
		wantGrown  bool
		wantBanner bool
		wantReads  int // 0 = unchecked
	}{
		{name: "an append grows the pane", target: tailTarget,
			results: []readResult{{doc: tailV1}, {doc: tailV2}}, signals: 1, wantGrown: true},
		{name: "a rewrite raises the banner", target: tailTarget,
			results: []readResult{{doc: tailV1}, {doc: rewritten}}, signals: 1, wantBanner: true},
		{name: "a new title raises the banner", target: tailTarget,
			results: []readResult{{doc: tailV1}, {doc: retitled}}, signals: 1, wantBanner: true},
		{name: "a new status raises the banner", target: tailTarget,
			results: []readResult{{doc: tailV1}, {doc: restatused}}, signals: 1, wantBanner: true},
		{name: "the same body raises the banner", target: tailTarget,
			results: []readResult{{doc: tailV1}, {doc: republished}}, signals: 1, wantBanner: true},
		{name: "a hidden tab grows from the pinned edition", target: tailTarget,
			results: []readResult{{doc: tailV2}}, pinned: map[string]readResult{"/x.md/v1": {doc: tailV1}}, wantGrown: true},
		{name: "a pruned edition raises the banner, read once", target: tailTarget,
			results: []readResult{{doc: tailV2}}, signals: 2, wantBanner: true, wantReads: 4},
		{name: "a stream without its pane raises the banner", target: liveTarget,
			results: []readResult{{doc: tailV1}, {doc: tailV2}}, signals: 1, wantBanner: true},
		{name: "a trail focused elsewhere raises the banner",
			target:  tailURL(t, "1", "soul.demarkus.io/d/other.md"),
			results: []readResult{{doc: tailV1}, {doc: tailV2}}, signals: 1, wantBanner: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			watcher := &fakeWatcher{signals: signalsThenClose(tt.signals)}
			reader := &scriptedReader{watcher: watcher, results: tt.results, pinned: tt.pinned}
			body := get(liveApp(t, LiveHandler{reader: reader, watcher: watcher}), tt.target).Body.String()
			added, banner := tailAdded(body), editionFrames(body) > 0
			if (added != "") != tt.wantGrown || banner != tt.wantBanner {
				t.Fatalf("grown %v banner %v, want %v and %v:\n%s", added != "", banner, tt.wantGrown, tt.wantBanner, body)
			}
			if tt.wantReads > 0 && reader.reads != tt.wantReads {
				t.Errorf("reads = %d, want %d: a lost edition is not read again", reader.reads, tt.wantReads)
			}
			if !tt.wantGrown {
				return
			}
			// Only what the append added, its links continuing the trail.
			if strings.Contains(added, "z.md") || !strings.Contains(added, "<h2>Later</h2>") ||
				!strings.Contains(added, `href="/t/soul.demarkus.io/d/x.md/~/soul.demarkus.io/d/y.md"`) {
				t.Errorf("added %q", added)
			}
			// The block re-arms at the new edition, keeping the pane, and this stream ends.
			if rearmed, want := streamURL(body), tailURL(t, "2", "soul.demarkus.io/d/x.md"); rearmed != want {
				t.Errorf("re-armed at %q, want %q", rearmed, want)
			}
			if strings.Contains(body, "event: done") {
				t.Error("done would close the new block's stream too")
			}
		})
	}
}

// What the tail adds is exactly what a reload of the new edition adds to the
// pane, so a grown pane and a reloaded one read the same.
func TestLiveTailMatchesAReload(t *testing.T) {
	t.Parallel()
	page := func(doc domain.Document) string {
		return get(liveCanvasApp(t, doc), "/t/soul.demarkus.io/d/x.md").Body.String()
	}
	before, after := page(tailV1), page(tailV2)
	watcher := &fakeWatcher{signals: signalsThenClose(1)}
	reader := &scriptedReader{watcher: watcher, results: []readResult{{doc: tailV1}, {doc: tailV2}}}
	body := get(liveApp(t, LiveHandler{reader: reader, watcher: watcher}), streamURL(before)).Body.String()
	added := tailAdded(body)
	if grown, reloaded := settled(paneContent(before)+" "+added), settled(paneContent(after)); added == "" || grown != reloaded {
		t.Errorf("grown pane differs from a reload:\ngrown    %q\nreloaded %q", grown, reloaded)
	}
}

// settled drops what differs between any two renders of one body: preview
// anchor names, minted per render, and whitespace between nodes.
func settled(markup string) string {
	return strings.Join(strings.Fields(previewAnchor.ReplaceAllString(markup, "--pv-N")), " ")
}

var previewAnchor = regexp.MustCompile(`--pv-\d+`)

// paneContent is the focused pane's body below its title.
func paneContent(page string) string {
	m := paneBodyRe.FindStringSubmatch(page)
	if m == nil {
		return ""
	}
	return m[1]
}

var paneBodyRe = regexp.MustCompile(`(?s)<article class="doc-body" id="live-doc"[^>]*>\s*<h1[^>]*>[^<]*</h1>\s*(.*?)\s*</article>`)
