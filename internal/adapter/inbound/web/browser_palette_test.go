//go:build browser && unix

package web

import (
	"context"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// scopedIndex answers a read of every world with a document from another
// world, so the page shows which scope the palette asked for.
type scopedIndex struct{ *fakeReading }

func (s scopedIndex) NameIndexCached(ctx context.Context, world string) ([]domain.IndexEntry, error) {
	entries, err := s.fakeReading.NameIndexCached(ctx, world)
	if world == "" {
		entries = append([]domain.IndexEntry{{Title: "Mission abroad", Path: "/mission.md", World: "other.io"}}, entries...)
	}
	return entries, err
}

// Typing in the palette swaps in the name matches and loads the section-text
// matches under them; the scope box widens the search to every world and back.
func TestBrowserPaletteSearches(t *testing.T) {
	svc := scopedIndex{&fakeReading{
		docs:      map[string]domain.Document{"/a.md": {Title: "A", Path: "/a.md", HTML: "<p>a</p>"}},
		nameIndex: []domain.IndexEntry{{Title: "Mission", Path: "/mission.md", World: "w.io"}},
	}}
	catalog := &fakeCatalog{result: domain.CatalogResult{BodyMatched: true, Hits: []domain.CatalogHit{{
		World: "w.io", Path: "/debugging.md", Title: "Debugging › Scope", Snippet: "mission creep, in the text",
	}}}}
	srv := browserServer(t, roomApp(t, NewRoom(svc, "w.io", "/a.md").WithTextSearch(catalog)))
	b := launchBrowser(t)
	b.navigate(t, srv.URL+"/t/w.io/d/a.md")

	b.eval(t, `document.dispatchEvent(new KeyboardEvent("keydown", {key: "k", ctrlKey: true, bubbles: true}))`)
	b.waitFor(t, `!document.getElementById("palette").hidden`)
	b.eval(t, `(() => { const i = document.getElementById("palette-input"); i.value = "mission"; i.dispatchEvent(new Event("input", {bubbles: true})); })()`)

	b.waitFor(t, `(() => { const row = document.querySelector("#palette-results .palette-title"); return !!row && row.textContent === "Mission"; })()`)
	b.waitFor(t, `(() => { const s = document.querySelector("#palette-results .palette-snippet"); return !!s && s.textContent.includes("in the text"); })()`)

	b.eval(t, `document.getElementById("palette-scope").click()`)
	b.waitFor(t, `[...document.querySelectorAll("#palette-results .palette-loc")].some(e => e.textContent.includes("other.io/mission.md"))`)
	b.eval(t, `document.getElementById("palette-scope").click()`)
	b.waitFor(t, `![...document.querySelectorAll("#palette-results .palette-loc")].some(e => e.textContent.includes("other.io"))`)
}
