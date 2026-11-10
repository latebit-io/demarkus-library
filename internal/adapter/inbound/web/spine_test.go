package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

func TestSpinePeekReadsLedeAndOutline(t *testing.T) {
	// 10 bytes of text before "Context", 33 before "Decision", 45 in all.
	rendered := `<p>0123456789</p><h2 id="context">Context</h2><p>abc</p>` +
		`<h3>Not a section</h3><h2 id="decision"><code>Decision</code></h2><p>wxyz</p>`
	peek := spinePeek(rendered)

	if peek.Lede != "0123456789" {
		t.Errorf("lede = %q", peek.Lede)
	}
	want := []spineSection{{Title: "Context", At: 22}, {Title: "Decision", At: 73}}
	if len(peek.Sections) != len(want) {
		t.Fatalf("sections = %+v, want %+v", peek.Sections, want)
	}
	for i, section := range peek.Sections {
		if section != want[i] {
			t.Errorf("section %d = %+v, want %+v", i, section, want[i])
		}
	}
}

func TestSpinePeekWithoutTextHasNoSections(t *testing.T) {
	if peek := spinePeek(`<h2></h2><hr>`); len(peek.Sections) != 0 || peek.Lede != "" {
		t.Errorf("peek = %+v, want empty", peek)
	}
}

func TestDocSpinePeeksOnlyAtDocuments(t *testing.T) {
	doc := domain.Document{Path: "/adr/0001.md", Type: "Decision", HTML: "<p>opens</p>"}
	spine := docSpine(paneAddr{Kind: paneDoc, World: "w.io", Value: "/adr/0001.md"}, doc)
	if spine.Kind != "Decision" || spine.Foot != "/adr" {
		t.Errorf("spine = %+v", spine)
	}
	if spine.Peek == nil || spine.Peek.Lede != "opens" {
		t.Errorf("peek = %+v", spine.Peek)
	}
	for _, addr := range []paneAddr{
		{Kind: paneDoc, World: "w.io", Value: "/adr/"},
		{Kind: paneTag, World: "w.io", Value: "adr"},
	} {
		if got := docSpine(addr, doc); got != nil {
			t.Errorf("%+v has a spine: %+v", addr, got)
		}
	}
}

func TestModifiedDayKeepsUnparsedValues(t *testing.T) {
	for modified, want := range map[string]string{
		"2026-07-26T22:13:19Z": "2026-07-26",
		"last tuesday":         "last tuesday",
		"":                     "",
	} {
		if got := modifiedDay(modified); got != want {
			t.Errorf("modifiedDay(%q) = %q, want %q", modified, got, want)
		}
	}
}

func TestNumberPanesCountsFromOne(t *testing.T) {
	panes := []paneVM{{Mode: "spine"}, {Mode: "body"}, {Mode: "focused"}}
	numberPanes(panes)
	for i, pane := range panes {
		if pane.Ordinal != i+1 {
			t.Errorf("pane %d ordinal = %d", i, pane.Ordinal)
		}
	}
}

func TestTrailSpineCarriesThumbIndexAndPeek(t *testing.T) {
	svc := &fakeReading{
		docs: map[string]domain.Document{
			"/adr/a.md": {Title: "Pane A", Path: "/adr/a.md", Type: "Decision", Status: "accepted", Modified: "2026-07-26T22:13:19Z",
				HTML: `<p>Opening line.</p><h2 id="context">Context</h2><p>more</p>`},
			"/b.md": {Title: "Pane B", Path: "/b.md", HTML: "<p>b</p>"},
			"/c.md": {Title: "Pane C", Path: "/c.md", HTML: "<p>c</p>"},
			"/d.md": {Title: "Pane D", Path: "/d.md", HTML: "<p>d</p>"},
		},
	}
	rec := get(readingApp(t, svc), "/t/w.io/d/adr/a.md/~/w.io/d/b.md/~/w.io/d/c.md/~/w.io/d/d.md?focus=2")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()

	for _, want := range []string{
		`data-key="w.io/adr/a.md"`, // the key islands.js files the reading under
		`<span class="spine-ord">1</span>`,
		`<span class="spine-kind">Decision</span>`,
		`<span class="spine-foot">/adr</span>`,
		`class="spine-tick" data-at="`,
		`<span class="peek-where">Decision · w.io/adr</span>`,
		`<span class="peek-lede">Opening line.</span>`,
		`modified 2026-07-26`,
		`>Context</span>`,
		`<span class="spine-ord">4</span>`, // pane D, right of the focus
	} {
		if !strings.Contains(body, want) {
			t.Errorf("canvas missing %q", want)
		}
	}
	// The peek never carries the body.
	if strings.Contains(body, "<p>more</p>") {
		t.Error("spine rendered its document body")
	}
}
