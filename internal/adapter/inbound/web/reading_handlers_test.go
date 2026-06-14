package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// fakeReading scripts the inbound port for handler tests. called is the
// last method invoked; calls is every invocation in order with its path or
// tag ("Read /x.md"). docs maps a path/tag to its scripted document
// (fallback: doc); errs maps a path/tag to a scripted error.
type fakeReading struct {
	doc      domain.Document
	docs     map[string]domain.Document
	errs     map[string]error
	raw      domain.RawDocument
	err      error
	floor    domain.Floor
	floorErr error

	worldMap    domain.WorldMap
	worldMapErr error

	draft      domain.EditDraft
	editErr    error
	publishErr error
	gotBody    string
	gotMeta    domain.PublishMeta
	gotVersion int
	backlink   map[string][]domain.Ref // keyed by path; the graph store's reverse edges
	neighbor   map[string]domain.Neighborhood
	recorded   map[string][]domain.Ref // path → links RecordLinks captured
	called     string
	calls      []string
	gotTag     string
}

func (f *fakeReading) record(method, key string) (domain.Document, error) {
	f.called = method
	f.calls = append(f.calls, method+" "+key)
	if err, ok := f.errs[key]; ok {
		return domain.Document{}, err
	}
	if doc, ok := f.docs[key]; ok {
		return doc, nil
	}
	return f.doc, f.err
}

func (f *fakeReading) Read(_ context.Context, _, path string) (domain.Document, error) {
	return f.record("Read", path)
}

func (f *fakeReading) Browse(_ context.Context, _, path string) (domain.Document, error) {
	return f.record("Browse", path)
}

func (f *fakeReading) History(_ context.Context, _, path string) (domain.Document, error) {
	return f.record("History", path)
}

func (f *fakeReading) Search(_ context.Context, _, _, q string) (domain.Document, error) {
	return f.record("Search", q)
}

func (f *fakeReading) Tag(_ context.Context, _, tag string) (domain.Document, error) {
	f.gotTag = tag
	return f.record("Tag", tag)
}

func (f *fakeReading) Raw(_ context.Context, _, _ string) (domain.RawDocument, error) {
	f.called = "Raw"
	return f.raw, f.err
}

func (f *fakeReading) ReadCached(_ context.Context, _, path string) (domain.Document, error) {
	return f.record("ReadCached", path)
}

func (f *fakeReading) BrowseCached(_ context.Context, _, path string) (domain.Document, error) {
	return f.record("BrowseCached", path)
}

func (f *fakeReading) TagCached(_ context.Context, _, tag string) (domain.Document, error) {
	f.gotTag = tag
	return f.record("TagCached", tag)
}

// RecordLinks is a write, not a read: it stays out of calls/called so the
// focused-live read-budget assertions keep measuring only world reads.
func (f *fakeReading) RecordLinks(_, path string, targets []domain.Ref) {
	if f.recorded == nil {
		f.recorded = map[string][]domain.Ref{}
	}
	f.recorded[path] = targets
}

func (f *fakeReading) Backlinks(_, path string) []domain.Ref {
	return f.backlink[path]
}

func (f *fakeReading) Neighborhood(_, path string) domain.Neighborhood {
	return f.neighbor[path]
}

func (f *fakeReading) Floor(context.Context) (domain.Floor, error) {
	f.called = "Floor"
	f.calls = append(f.calls, "Floor")
	return f.floor, f.floorErr
}

func (f *fakeReading) FloorCached(context.Context) (domain.Floor, error) {
	f.called = "FloorCached"
	f.calls = append(f.calls, "FloorCached")
	return f.floor, f.floorErr
}

func (f *fakeReading) WorldMap(_ context.Context, _ string) (domain.WorldMap, error) {
	f.called = "WorldMap"
	f.calls = append(f.calls, "WorldMap")
	return f.worldMap, f.worldMapErr
}

func (f *fakeReading) WorldMapCached(_ context.Context, _ string) (domain.WorldMap, error) {
	f.called = "WorldMapCached"
	f.calls = append(f.calls, "WorldMapCached")
	return f.worldMap, f.worldMapErr
}

func (f *fakeReading) EditDraft(_ context.Context, _, path string) (domain.EditDraft, error) {
	f.called = "EditDraft"
	f.calls = append(f.calls, "EditDraft "+path)
	if f.editErr != nil {
		return domain.EditDraft{}, f.editErr
	}
	return f.draft, nil
}

func (f *fakeReading) Preview(markdown string) (domain.Rendered, error) {
	f.called = "Preview"
	f.gotBody = markdown
	return domain.Rendered{HTML: "<p>" + markdown + "</p>"}, nil
}

func (f *fakeReading) Publish(_ context.Context, _, path, body string, meta domain.PublishMeta, expectedVersion int) (domain.Document, error) {
	f.called = "Publish"
	f.calls = append(f.calls, "Publish "+path)
	f.gotBody = body
	f.gotMeta = meta
	f.gotVersion = expectedVersion
	if f.publishErr != nil {
		return domain.Document{}, f.publishErr
	}
	return f.doc, nil
}

func readingApp(t *testing.T, svc *fakeReading) *echo.Echo {
	t.Helper()
	app := echo.New()
	view, err := NewView()
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}
	app.Renderer = view
	ReadingRoutes(app, NewReadingHandler(svc, "soul.demarkus.io", "/index.md"))
	return app
}

func get(app *echo.Echo, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestDocRendersMargin(t *testing.T) {
	svc := &fakeReading{doc: domain.Document{
		Title:      "ADR 7",
		Source:     "soul.demarkus.io:6309",
		Path:       "/adr/0007.md",
		HTML:       "<p>body</p>",
		Status:     "accepted",
		Tags:       []string{"adr", "decision"},
		Properties: []domain.Property{{Key: "author", Value: "fritz"}},
		Modified:   "2026-06-12T10:00:00Z",
		Version:    "7",
		Agent:      "claude-code",
	}}
	body := get(readingApp(t, svc), "/w/soul.demarkus.io/d/adr/0007.md").Body.String()

	for _, want := range []string{
		`class="status status-accepted"`,
		`href="/w/soul.demarkus.io/tags/adr"`,
		`href="/w/soul.demarkus.io/tags/decision"`,
		`<dt>author</dt><dd>fritz</dd>`,
		"2026-06-12T10:00:00Z",
		"claude-code",
		"mark://soul.demarkus.io/adr/0007.md",
		`href="/w/soul.demarkus.io/raw/adr/0007.md"`,
		`href="/w/soul.demarkus.io/versions/adr/0007.md"`,
		`href="/w/soul.demarkus.io/g/adr/0007.md">graph`,
		`href="/w/soul.demarkus.io/u">map`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("doc page missing %q", want)
		}
	}
	if strings.Contains(body, `role="search"`) || strings.Contains(body, "type=\"search\"") {
		t.Errorf("global search box must be gone (ADR 0005 decision 5)")
	}
}

func TestDocMarginOmitsStatusAxisTag(t *testing.T) {
	svc := &fakeReading{doc: domain.Document{
		Path: "/x.md", Status: "accepted", Tags: []string{"status:accepted", "adr"},
	}}
	body := get(readingApp(t, svc), "/w/soul.demarkus.io/d/x.md").Body.String()
	if strings.Contains(body, "tags/status") {
		t.Errorf("status: axis tag must not appear in the tag list (the badge carries it)")
	}
	if !strings.Contains(body, "/tags/adr") {
		t.Errorf("ordinary tag missing from tag list")
	}
}

func TestBrowseRendersWithoutMargin(t *testing.T) {
	svc := &fakeReading{doc: domain.Document{Title: "Index of /plans/", Path: "/plans/", HTML: "<ul></ul>"}}
	body := get(readingApp(t, svc), "/w/soul.demarkus.io/d/plans/").Body.String()
	if svc.called != "Browse" {
		t.Fatalf("routed to %s, want Browse", svc.called)
	}
	if strings.Contains(body, `class="doc-meta"`) {
		t.Errorf("listing must not render the margin metadata block")
	}
}

func TestTagPageRoutesToTag(t *testing.T) {
	svc := &fakeReading{doc: domain.Document{Title: "Tagged: adr", HTML: "<table></table>"}}
	rec := get(readingApp(t, svc), "/w/soul.demarkus.io/tags/adr")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if svc.called != "Tag" || svc.gotTag != "adr" {
		t.Errorf("called %s(%q), want Tag(adr)", svc.called, svc.gotTag)
	}
}

func TestTagPageUnescapesTag(t *testing.T) {
	svc := &fakeReading{doc: domain.Document{}}
	get(readingApp(t, svc), "/w/soul.demarkus.io/tags/category%3Areference")
	if svc.gotTag != "category:reference" {
		t.Errorf("tag = %q, want category:reference", svc.gotTag)
	}
}

func TestRawSourceServesPlainText(t *testing.T) {
	src := "---\nstatus: draft\n---\n# Raw\n<script>x</script>"
	svc := &fakeReading{raw: domain.RawDocument{Body: src}}
	rec := get(readingApp(t, svc), "/w/soul.demarkus.io/raw/x.md")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content-type = %q, want text/plain (never HTML — the body is untrusted)", ct)
	}
	if rec.Body.String() != src {
		t.Errorf("raw body altered: %q", rec.Body.String())
	}
}

func TestRawSourceMapsErrors(t *testing.T) {
	svc := &fakeReading{err: domain.ErrNotFound}
	if rec := get(readingApp(t, svc), "/w/soul.demarkus.io/raw/x.md"); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	svc = &fakeReading{err: domain.ErrUnauthorized}
	if rec := get(readingApp(t, svc), "/w/soul.demarkus.io/raw/x.md"); rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestPageHasNoSearchBox(t *testing.T) {
	svc := &fakeReading{doc: domain.Document{Title: "Home", Path: "/index.md"}}
	body := get(readingApp(t, svc), "/").Body.String()
	if strings.Contains(body, "input") {
		t.Errorf("no input elements expected on the page chrome: search box was removed")
	}
}
