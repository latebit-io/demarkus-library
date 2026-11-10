package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// fakeCatalog answers the palette's section-text search.
type fakeCatalog struct {
	result domain.CatalogResult
	err    error
	got    []domain.CatalogQuery
}

func (f *fakeCatalog) Lookup(_ context.Context, q domain.CatalogQuery) (domain.CatalogResult, error) {
	f.got = append(f.got, q)
	return f.result, f.err
}

// paletteGet is an htmx GET from a reader on world-a's index.
func paletteGet(t *testing.T, room Room, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, http.NoBody)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Current-URL", "http://x/t/world-a/d/index.md")
	rec := httptest.NewRecorder()
	roomApp(t, room).ServeHTTP(rec, req)
	return rec
}

func TestPaletteScopeAndTextFragment(t *testing.T) {
	svc := &fakeReading{nameIndex: []domain.IndexEntry{{Title: "Mission", Path: "/mission.md", World: "world-b"}}}
	room := NewRoom(svc, "soul.demarkus.io", "/index.md").WithTextSearch(&fakeCatalog{})

	body := paletteGet(t, room, "/palette?q=mission&scope=universe").Body.String()
	if !slices.Contains(svc.calls, "NameIndexCached") || svc.indexWorld != "" {
		t.Errorf("index read = %v of %q, want the cached index of every world", svc.calls, svc.indexWorld)
	}
	if !strings.Contains(body, `hx-get="/palette/text?q=mission&amp;scope=universe"`) {
		t.Errorf("fragment should load the text matches for the same scope: %s", body)
	}
	if body := paletteGet(t, room, "/palette?q=mission").Body.String(); !strings.Contains(body, `hx-get="/palette/text?q=mission&amp;world=world-a"`) {
		t.Errorf("fragment should load the text matches of the reader's world: %s", body)
	}

	// A short query is not searched in text, nor is any query without a text search.
	if body := paletteGet(t, room, "/palette?q=mi").Body.String(); strings.Contains(body, "/palette/text") {
		t.Errorf("two characters should not load text matches: %s", body)
	}
	bare := NewRoom(svc, "soul.demarkus.io", "/index.md")
	if body := paletteGet(t, bare, "/palette?q=mission").Body.String(); strings.Contains(body, "/palette/text") {
		t.Errorf("no text search, no text matches: %s", body)
	}
	if rec := paletteGet(t, bare, "/palette/text?q=mission"); rec.Code != http.StatusNoContent {
		t.Errorf("text without a text search = %d, want 204", rec.Code)
	}
}

func TestPaletteTextRendersSectionMatches(t *testing.T) {
	catalog := &fakeCatalog{result: domain.CatalogResult{BodyMatched: true, Hits: []domain.CatalogHit{{
		World: "world-a", Path: "/debugging.md", Anchor: "poison-lock",
		Title: "Debugging › Poison lock", Snippet: "ignoring the lock pid write creates a poison lock",
	}}}}
	room := NewRoom(&fakeReading{}, "soul.demarkus.io", "/index.md").WithTextSearch(catalog)

	rec := paletteGet(t, room, "/palette/text?q=poison+lock&scope=world&world=world-a")
	body := rec.Body.String()
	// The link opens the section: a heading's id is its anchor.
	for _, want := range []string{"In the text", "Debugging › Poison lock", "creates a poison lock", `href="/t/world-a/d/index.md/~/world-a/d/debugging.md#poison-lock"`} {
		if !strings.Contains(body, want) {
			t.Errorf("text fragment missing %q: %s", want, body)
		}
	}
	want := domain.CatalogQuery{World: "world-a", Query: "poison lock", Match: domain.MatchBody, Limit: paletteTextRows}
	if len(catalog.got) != 1 || catalog.got[0] != want {
		t.Errorf("lookup = %+v, want %+v", catalog.got, want)
	}

	// The universe scope searches every world.
	paletteGet(t, room, "/palette/text?q=poison+lock&scope=universe")
	if got := catalog.got[len(catalog.got)-1]; got.World != "" {
		t.Errorf("universe text search world = %q, want every world", got.World)
	}

	// A world without section search answers from the catalog: nothing to add.
	catalog.result.BodyMatched = false
	if rec := paletteGet(t, room, "/palette/text?q=poison+lock&world=world-a"); rec.Code != http.StatusNoContent {
		t.Errorf("catalog fallback = %d %q, want 204", rec.Code, rec.Body.String())
	}
	// No hits render nothing, and a direct (non-htmx) hit is not a page.
	catalog.result = domain.CatalogResult{BodyMatched: true}
	if body := paletteGet(t, room, "/palette/text?q=poison+lock&world=world-a").Body.String(); strings.TrimSpace(body) != "" {
		t.Errorf("no hits = %q, want an empty fragment", body)
	}
	if rec := get(roomApp(t, room), "/palette/text?q=poison+lock"); rec.Code != http.StatusNoContent {
		t.Errorf("non-htmx text = %d, want 204", rec.Code)
	}
}
