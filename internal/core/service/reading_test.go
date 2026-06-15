package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// fakeGateway and fakeRenderer stand in for the outbound adapters. When called
// is non-nil each method records its own name, so tests can assert that a service
// method routes to the right gateway verb.
type fakeGateway struct {
	raw       domain.RawDocument
	err       error
	called    *string
	filter    *string // records the Lookup filter argument
	worlds    []domain.WorldInfo
	worldsErr error
	fetchBody map[string]string // path → body for Fetch (e.g. the hub /graph.md); falls back to raw

	publishVersion int
	publishErr     error
}

func (f fakeGateway) record(name string) {
	if f.called != nil {
		*f.called = name
	}
}

func (f fakeGateway) Fetch(_ context.Context, _, path string) (domain.RawDocument, error) {
	f.record("Fetch")
	if body, ok := f.fetchBody[path]; ok {
		return domain.RawDocument{Path: path, Body: body}, nil
	}
	return f.raw, f.err
}
func (f fakeGateway) List(context.Context, string, string) (domain.RawDocument, error) {
	f.record("List")
	return f.raw, f.err
}
func (f fakeGateway) Versions(context.Context, string, string) (domain.RawDocument, error) {
	f.record("Versions")
	return f.raw, f.err
}
func (f fakeGateway) Lookup(_ context.Context, _, _, _, filter string) (domain.RawDocument, error) {
	f.record("Lookup")
	if f.filter != nil {
		*f.filter = filter
	}
	return f.raw, f.err
}
func (f fakeGateway) Worlds(context.Context) ([]domain.WorldInfo, error) {
	f.record("Worlds")
	return f.worlds, f.worldsErr
}
func (f fakeGateway) Publish(_ context.Context, _, _, _ string, _ domain.PublishMeta, _ int) (int, error) {
	f.record("Publish")
	return f.publishVersion, f.publishErr
}
func (f fakeGateway) Append(_ context.Context, _, _, _ string) (int, error) {
	f.record("Append")
	return f.publishVersion, f.publishErr
}

type fakeRenderer struct {
	html  string
	props []domain.Property
	err   error
}

func (f fakeRenderer) Render(string) (domain.Rendered, error) {
	return domain.Rendered{HTML: f.html, Properties: f.props}, f.err
}

func TestReadRendersAndPopulatesDocument(t *testing.T) {
	svc := newTestService(
		fakeGateway{raw: domain.RawDocument{
			Source: "world:6309",
			Path:   "/greeting.md",
			Body:   "# Hi",
			Metadata: map[string]string{
				"title":    "Greeting",
				"tags":     "demarkus, status:accepted, hello",
				"modified": "2026-06-12T10:00:00Z",
				"version":  "7",
				"agent":    "claude-code",
			},
		}},
		fakeRenderer{html: "<h1>Hi</h1>", props: []domain.Property{{Key: "author", Value: "fritz"}}},
	)

	doc, err := svc.Read(t.Context(), "soul", "/greeting.md")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if doc.Title != "Greeting" {
		t.Errorf("title = %q, want Greeting", doc.Title)
	}
	if doc.Source != "world:6309" {
		t.Errorf("source = %q, want world:6309", doc.Source)
	}
	if doc.HTML != "<h1>Hi</h1>" {
		t.Errorf("html = %q", doc.HTML)
	}
	if doc.Status != "accepted" {
		t.Errorf("status = %q, want accepted (metadata tag axis)", doc.Status)
	}
	if len(doc.Tags) != 3 || doc.Tags[0] != "demarkus" || doc.Tags[2] != "hello" {
		t.Errorf("tags = %v", doc.Tags)
	}
	if doc.Modified != "2026-06-12T10:00:00Z" || doc.Version != "7" || doc.Agent != "claude-code" {
		t.Errorf("provenance = %q/%q/%q", doc.Modified, doc.Version, doc.Agent)
	}
	if len(doc.Properties) != 1 || doc.Properties[0].Key != "author" {
		t.Errorf("properties = %v", doc.Properties)
	}
}

func TestResolveStatusAuthorityOrder(t *testing.T) {
	cases := []struct {
		name  string
		tags  []string
		props []domain.Property
		want  string
	}{
		{"metadata axis wins", []string{"a", "status:accepted"}, []domain.Property{{Key: "status", Value: "draft"}}, "accepted"},
		{"frontmatter fallback", []string{"a", "b"}, []domain.Property{{Key: "status", Value: "WIP"}}, "wip"},
		{"absent is draft", []string{"a"}, nil, "draft"},
		{"unsafe value is draft", []string{"status:<script>"}, nil, "draft"},
		{"empty axis value is draft", []string{"status:"}, nil, "draft"},
		{"archived passes", []string{"status:archived"}, nil, "archived"},
	}
	for _, tc := range cases {
		if got := resolveStatus(tc.tags, tc.props); got != tc.want {
			t.Errorf("%s: resolveStatus = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestBrowseHistorySearchRouteAndRender(t *testing.T) {
	raw := domain.RawDocument{Source: "world:6309", Path: "/plans/", Body: "- a\n- b"}

	cases := []struct {
		name       string
		call       func(*ReadingService) (domain.Document, error)
		wantTitle  string
		wantMethod string // gateway verb the service must route to
	}{
		{"Browse", func(s *ReadingService) (domain.Document, error) { return s.Browse(t.Context(), "soul", "/plans/") }, "Index of /plans/", "List"},
		{"History", func(s *ReadingService) (domain.Document, error) { return s.History(t.Context(), "soul", "/x.md") }, "Editions of /x.md", "Versions"},
		{"Search", func(s *ReadingService) (domain.Document, error) { return s.Search(t.Context(), "soul", "/", "hex") }, "Catalog: hex", "Lookup"},
		{"Tag", func(s *ReadingService) (domain.Document, error) { return s.Tag(t.Context(), "soul", "adr") }, "Tagged: adr", "Lookup"},
	}
	for _, tc := range cases {
		var called string
		svc := newTestService(fakeGateway{raw: raw, called: &called}, fakeRenderer{html: "<ul></ul>"})

		doc, err := tc.call(svc)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if called != tc.wantMethod {
			t.Errorf("%s routed to gateway.%s, want gateway.%s", tc.name, called, tc.wantMethod)
		}
		if doc.Title != tc.wantTitle {
			t.Errorf("%s title = %q, want %q", tc.name, doc.Title, tc.wantTitle)
		}
		if doc.HTML != "<ul></ul>" {
			t.Errorf("%s html = %q", tc.name, doc.HTML)
		}
		if doc.Status != "" {
			t.Errorf("%s status = %q, want empty (synthetic views carry no margin)", tc.name, doc.Status)
		}
	}
}

func TestTagFiltersLookup(t *testing.T) {
	var filter string
	svc := newTestService(fakeGateway{filter: &filter}, fakeRenderer{})
	if _, err := svc.Tag(t.Context(), "soul", "adr"); err != nil {
		t.Fatalf("Tag: %v", err)
	}
	if filter != "tag=adr" {
		t.Errorf("filter = %q, want tag=adr", filter)
	}
}

func TestSearchDoesNotFilter(t *testing.T) {
	var filter string
	svc := newTestService(fakeGateway{filter: &filter}, fakeRenderer{})
	if _, err := svc.Search(t.Context(), "soul", "/", "hex"); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if filter != "" {
		t.Errorf("filter = %q, want empty", filter)
	}
}

func TestRawReturnsUnrenderedSource(t *testing.T) {
	raw := domain.RawDocument{
		Source:   "world:6309",
		Path:     "/x.md",
		Body:     "---\nstatus: draft\n---\n# Source",
		Metadata: map[string]string{"version": "3"},
	}
	svc := newTestService(fakeGateway{raw: raw}, fakeRenderer{err: errors.New("renderer must not run")})
	got, err := svc.Raw(t.Context(), "soul", "/x.md")
	if err != nil {
		t.Fatalf("Raw: %v", err)
	}
	if got.Body != raw.Body {
		t.Errorf("body = %q, want untouched source (frontmatter included)", got.Body)
	}
}

func TestReadPropagatesGatewayError(t *testing.T) {
	for _, want := range []error{domain.ErrNotFound, domain.ErrUnauthorized} {
		svc := newTestService(fakeGateway{err: want}, fakeRenderer{})
		if _, err := svc.Read(t.Context(), "soul", "/x.md"); !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	}
}

func TestReadPropagatesRenderError(t *testing.T) {
	boom := errors.New("render boom")
	svc := newTestService(
		fakeGateway{raw: domain.RawDocument{Body: "x"}},
		fakeRenderer{err: boom},
	)
	if _, err := svc.Read(t.Context(), "soul", "/x.md"); !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
}

func TestTitleFallsBackToBasename(t *testing.T) {
	svc := newTestService(
		fakeGateway{raw: domain.RawDocument{Path: "/notes/deploy.md"}},
		fakeRenderer{html: ""},
	)
	doc, err := svc.Read(t.Context(), "soul", "/notes/deploy.md")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if doc.Title != "deploy" {
		t.Errorf("title = %q, want deploy", doc.Title)
	}
}

func TestTitleFromMetadataIsTrimmed(t *testing.T) {
	if got := titleFor("/x.md", map[string]string{"title": "  Spaced  "}); got != "Spaced" {
		t.Errorf("got %q, want Spaced", got)
	}
	if !strings.HasPrefix(titleFor("/a/b/c.md", nil), "c") {
		t.Errorf("basename fallback broken")
	}
}

// newTestService wires the fakes with no cache — cache behavior has its own
// tests below.
func newTestService(g fakeGateway, r fakeRenderer) *ReadingService {
	return NewReadingService(g, r, nil)
}

// fakeCache records puts and serves scripted hits.
type fakeCache struct {
	store map[string]domain.Document
	puts  []string
}

func newFakeCache() *fakeCache { return &fakeCache{store: map[string]domain.Document{}} }

func (f *fakeCache) Get(key string) (domain.Document, bool) {
	doc, ok := f.store[key]
	return doc, ok
}

func (f *fakeCache) Put(key string, doc domain.Document) {
	f.store[key] = doc
	f.puts = append(f.puts, key)
}

func TestReadRefreshesCacheAndReadCachedServesIt(t *testing.T) {
	c := newFakeCache()
	var called string
	svc := NewReadingService(
		fakeGateway{raw: domain.RawDocument{Path: "/x.md", Body: "# x"}, called: &called},
		fakeRenderer{html: "<h1>x</h1>"}, c)

	// Live read populates the cache (focused-live policy).
	if _, err := svc.Read(t.Context(), "soul", "/x.md"); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(c.puts) != 1 {
		t.Fatalf("puts = %v, want exactly the doc key", c.puts)
	}

	// Cached read is a pure cache hit — no gateway call.
	called = ""
	doc, err := svc.ReadCached(t.Context(), "soul", "/x.md")
	if err != nil {
		t.Fatalf("ReadCached: %v", err)
	}
	if called != "" {
		t.Errorf("gateway hit on cached read: %s", called)
	}
	if doc.HTML != "<h1>x</h1>" {
		t.Errorf("doc = %+v", doc)
	}

	// Miss reads through (and re-populates).
	if _, err := svc.ReadCached(t.Context(), "soul", "/other.md"); err != nil {
		t.Fatalf("ReadCached miss: %v", err)
	}
	if called != "Fetch" {
		t.Errorf("miss did not read through, called = %q", called)
	}
}

func TestTagCachedReadsThroughAndCaches(t *testing.T) {
	c := newFakeCache()
	var called string
	svc := NewReadingService(fakeGateway{raw: domain.RawDocument{Body: "|t|"}, called: &called},
		fakeRenderer{html: "<table></table>"}, c)

	if _, err := svc.TagCached(t.Context(), "soul", "adr"); err != nil {
		t.Fatalf("TagCached: %v", err)
	}
	if called != "Lookup" {
		t.Fatalf("called = %q", called)
	}
	called = ""
	if _, err := svc.TagCached(t.Context(), "soul", "adr"); err != nil {
		t.Fatalf("TagCached hit: %v", err)
	}
	if called != "" {
		t.Errorf("gateway hit on cached tag page: %s", called)
	}
}

func TestDocAndTagKeysNeverCollide(t *testing.T) {
	if docKey("w", "/adr") == tagKey("w", "/adr") {
		t.Error("doc and tag cache keys collide")
	}
}

func TestNilCacheCachedReadsFallThrough(t *testing.T) {
	var called string
	svc := newTestService(fakeGateway{raw: domain.RawDocument{Body: "x"}, called: &called}, fakeRenderer{})
	if _, err := svc.ReadCached(t.Context(), "soul", "/x.md"); err != nil {
		t.Fatalf("ReadCached: %v", err)
	}
	if called != "Fetch" {
		t.Errorf("nil-cache ReadCached must read live, called = %q", called)
	}
}
