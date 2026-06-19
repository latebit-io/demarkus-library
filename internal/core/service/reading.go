// Package service contains the application core — the hexagon. It implements the
// inbound ports using only outbound ports, so it has no knowledge of Echo, QUIC,
// or goldmark. This is where the "librarian" intelligence lives.
package service

import (
	"context"
	"regexp"
	"strings"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus-library/internal/core/port"
)

// ReadingService implements port.ReadingService over a world gateway, a
// renderer, and an optional rendered-document cache (nil = uncached; the
// *Cached methods then read live).
type ReadingService struct {
	world     port.WorldGateway
	renderer  port.Renderer
	cache     port.DocumentCache
	floor     floorCache
	worldMaps worldMapCache
	graph     linkGraph
	hub       string // topology-source world (DEMARKUS_HUB); "" disables hub enrichment
}

// compile-time check that ReadingService satisfies the inbound port.
var _ port.ReadingService = (*ReadingService)(nil)

// NewReadingService wires the outbound ports into the core. cache may be nil.
func NewReadingService(world port.WorldGateway, renderer port.Renderer, cache port.DocumentCache) *ReadingService {
	return &ReadingService{world: world, renderer: renderer, cache: cache}
}

// WithHub sets the topology-source world for floor enrichment (DEMARKUS_HUB)
// and returns the service for chaining at the composition root. Empty leaves
// the floor on its mark_worlds + observed-links baseline. Kept off the
// constructor so existing callers (and tests) are untouched.
func (s *ReadingService) WithHub(hub string) *ReadingService {
	s.hub = hub
	return s
}

// Read fetches a document from the world and renders it to sanitized HTML
// plus the margin's trust signals (ADR 0005 decisions 6–8): status, tags,
// provenance, and the document-properties block parsed from frontmatter.
func (s *ReadingService) Read(ctx context.Context, world, path string) (domain.Document, error) {
	raw, err := s.world.Fetch(ctx, world, path)
	if err != nil {
		return domain.Document{}, err
	}
	rendered, err := s.renderer.Render(raw.Body)
	if err != nil {
		return domain.Document{}, err
	}
	tags := splitTags(raw.Metadata["tags"])
	doc := domain.Document{
		Title:      titleFor(raw.Path, raw.Metadata, rendered.Title),
		Source:     raw.Source,
		Path:       raw.Path,
		HTML:       rendered.HTML,
		Status:     resolveStatus(tags, rendered.Properties),
		Tags:       tags,
		Properties: rendered.Properties,
		Modified:   raw.Metadata["modified"],
		Version:    raw.Metadata["version"],
		Agent:      raw.Metadata["agent"],
	}
	s.cachePut(docKey(world, path), doc)
	return doc, nil
}

// Open reads (world, path), dispatching to Browse for a listing path and Read
// for a document. The single read-side owner of the listing-vs-document
// addressing rule (domain.IsListingPath) — callers address a resource without
// re-deciding it. OpenCached is its cached counterpart for unfocused panes.
func (s *ReadingService) Open(ctx context.Context, world, path string) (domain.Document, error) {
	if domain.IsListingPath(path) {
		return s.Browse(ctx, world, path)
	}
	return s.Read(ctx, world, path)
}

// OpenCached is Open served from the rendered-document cache (trail engine's
// unfocused panes), dispatching Browse/Read by path shape like Open.
func (s *ReadingService) OpenCached(ctx context.Context, world, path string) (domain.Document, error) {
	if domain.IsListingPath(path) {
		return s.BrowseCached(ctx, world, path)
	}
	return s.ReadCached(ctx, world, path)
}

// ReadCached serves (world, path) from the rendered-document cache, reading
// through on a miss. Trail engine: unfocused panes only — the focused pane
// goes through Read, which refreshes the entry (focused-live policy).
func (s *ReadingService) ReadCached(ctx context.Context, world, path string) (domain.Document, error) {
	if s.cache != nil {
		if doc, ok := s.cache.Get(docKey(world, path)); ok {
			return doc, nil
		}
	}
	return s.Read(ctx, world, path)
}

// TagCached is ReadCached for tag-page panes.
func (s *ReadingService) TagCached(ctx context.Context, world, tag string) (domain.Document, error) {
	if s.cache != nil {
		if doc, ok := s.cache.Get(tagKey(world, tag)); ok {
			return doc, nil
		}
	}
	return s.Tag(ctx, world, tag)
}

func (s *ReadingService) cachePut(key string, doc domain.Document) {
	if s.cache != nil {
		s.cache.Put(key, doc)
	}
}

// Cache keys are kind-prefixed so a document and a tag page can never
// collide. The world is delimited with a NUL-adjacent separator that cannot
// appear in either world names or paths/tags as routed.
func docKey(world, path string) string { return "d\x00" + world + "\x00" + path }
func tagKey(world, tag string) string  { return "t\x00" + world + "\x00" + tag }

// Browse renders a directory listing (the stacks) at path.
func (s *ReadingService) Browse(ctx context.Context, world, path string) (domain.Document, error) {
	raw, err := s.world.List(ctx, world, path)
	if err != nil {
		return domain.Document{}, err
	}
	doc, err := s.render(raw, "Index of "+path)
	if err != nil {
		return domain.Document{}, err
	}
	s.cachePut(docKey(world, path), doc)
	return doc, nil
}

// BrowseCached is ReadCached for listing panes. Listing paths end in a
// slash and document paths never do, so they share the doc key space
// without collisions.
func (s *ReadingService) BrowseCached(ctx context.Context, world, path string) (domain.Document, error) {
	if s.cache != nil {
		if doc, ok := s.cache.Get(docKey(world, path)); ok {
			return doc, nil
		}
	}
	return s.Browse(ctx, world, path)
}

// History renders the edition history of the document at path.
func (s *ReadingService) History(ctx context.Context, world, path string) (domain.Document, error) {
	raw, err := s.world.Versions(ctx, world, path)
	if err != nil {
		return domain.Document{}, err
	}
	return s.render(raw, "Editions of "+path)
}

// Search renders the card catalog (LOOKUP) results for query under scope.
func (s *ReadingService) Search(ctx context.Context, world, scope, query string) (domain.Document, error) {
	raw, err := s.world.Lookup(ctx, world, scope, query, "")
	if err != nil {
		return domain.Document{}, err
	}
	return s.render(raw, "Catalog: "+query)
}

// Tag renders the card catalog filtered to one tag — the lookup-backed tag
// page (ADR 0005 decision 5). The tag rides as both query and filter: the
// filter does the exact narrowing, the query keeps ranking sensible.
func (s *ReadingService) Tag(ctx context.Context, world, tag string) (domain.Document, error) {
	raw, err := s.world.Lookup(ctx, world, "/", tag, "tag="+tag)
	if err != nil {
		return domain.Document{}, err
	}
	doc, err := s.render(raw, "Tagged: "+tag)
	if err != nil {
		return domain.Document{}, err
	}
	s.cachePut(tagKey(world, tag), doc)
	return doc, nil
}

// Raw returns the unrendered source of the document at (world, path) — the
// projection's escape to protocol (ADR 0005 decision 12). No rendering, no
// frontmatter strip: this is the real thing.
func (s *ReadingService) Raw(ctx context.Context, world, path string) (domain.RawDocument, error) {
	return s.world.Fetch(ctx, world, path)
}

// render turns a raw markdown document into a display-ready Document. Used by
// the synthetic views (listings, editions, catalog), which carry no margin —
// an empty margin is correct.
func (s *ReadingService) render(raw domain.RawDocument, title string) (domain.Document, error) {
	rendered, err := s.renderer.Render(raw.Body)
	if err != nil {
		return domain.Document{}, err
	}
	return domain.Document{
		Title:  title,
		Source: raw.Source,
		Path:   raw.Path,
		HTML:   rendered.HTML,
	}, nil
}

// titleFor picks the document title in authority order: the cataloged metadata
// title, then the body's leading H1 (lifted out by the renderer), then the path
// basename. The bodyTitle step means a doc whose source opens with "# Patterns"
// shows "Patterns" once — not the lowercase filename above a duplicate H1.
func titleFor(path string, meta map[string]string, bodyTitle string) string {
	if t := strings.TrimSpace(meta["title"]); t != "" {
		return t
	}
	if bodyTitle != "" {
		return bodyTitle
	}
	name := path[strings.LastIndex(path, "/")+1:]
	if name == "" {
		return path
	}
	return strings.TrimSuffix(name, ".md")
}

// splitTags parses the catalog's comma-separated tags metadata.
func splitTags(tags string) []string {
	var out []string
	for t := range strings.SplitSeq(tags, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// statusValue bounds what the trust badge will display: the documented
// vocabulary is draft|wip|accepted|archived, but the axis is open — any
// css-class-safe token renders verbatim. Anything else is treated as
// unlabeled.
var statusValue = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// resolveStatus applies ADR 0005 decision 6's authority order: the demarkus
// metadata status: tag axis wins, body-frontmatter status: is the fallback,
// and absent means draft — unlabeled reads as untrusted, the correct failure
// mode.
func resolveStatus(tags []string, props []domain.Property) string {
	for _, t := range tags {
		if v, ok := strings.CutPrefix(t, "status:"); ok {
			return normalizeStatus(v)
		}
	}
	for _, p := range props {
		if strings.EqualFold(strings.TrimSpace(p.Key), "status") {
			return normalizeStatus(p.Value)
		}
	}
	return "draft"
}

func normalizeStatus(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if !statusValue.MatchString(v) {
		return "draft"
	}
	return v
}
