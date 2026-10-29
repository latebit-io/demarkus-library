// Package service contains the application core — the hexagon. It implements the
// inbound ports using only outbound ports, so it has no knowledge of Echo, QUIC,
// or goldmark. This is where the "librarian" intelligence lives.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
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
	protocol  port.Renderer // the protocol's own views: listings, editions, catalog
	cache     port.DocumentCache
	floor     ttlCache[domain.Floor]
	worldMaps ttlCache[domain.WorldMap]
	names     ttlCache[[]domain.IndexEntry] // per-world name index, for unfocused listings
	graph     observedLinks                 // render-time links, shared by every reader; reads take the reader's mask
	hosts     ttlCache[hostIndex]           // the reader's world index: the graph mask and the hub's host join
	hub       string                        // topology-source world (DEMARKUS_HUB); "" disables hub enrichment
	hubs      ttlCache[hubGraph]            // one hub read per reader per hubTTL, shared by every consumer
	watchers  documentWatchers              // live-edition streams waiting on a document (changes.go)
	fence     docFence                      // keeps a hint's stale edition out of cache (changes.go)
	// graphParser decodes the hub's published /graph.md; nil (like hub "")
	// leaves the floor on its mark_worlds + observed-links baseline.
	graphParser port.GraphExportParser
}

// compile-time check that ReadingService satisfies the inbound port.
var _ port.ReadingService = (*ReadingService)(nil)

// compile-time check that ReadingService also serves the librarian's catalog.
var _ port.Catalog = (*ReadingService)(nil)

// compile-time check that ReadingService serves the live-edition streams.
var _ port.DocumentWatcher = (*ReadingService)(nil)

// NewReadingService wires the outbound ports into the core. cache may be nil.
func NewReadingService(world port.WorldGateway, renderer port.Renderer, cache port.DocumentCache) *ReadingService {
	return &ReadingService{
		world: world, renderer: renderer, protocol: renderer, cache: cache,
		floor:     ttlCache[domain.Floor]{retain: floorRetain},
		worldMaps: ttlCache[domain.WorldMap]{retain: worldMapTTL},
		names:     ttlCache[[]domain.IndexEntry]{retain: floorTTL},
		hosts:     ttlCache[hostIndex]{retain: hubTTL},
		hubs:      ttlCache[hubGraph]{retain: hubTTL},
	}
}

// WithHub sets the topology-source world for floor enrichment (DEMARKUS_HUB)
// and the parser for its published graph export, returning the service for
// chaining at the composition root. An empty hub or nil parser leaves the
// floor on its mark_worlds + observed-links baseline. Kept off the
// constructor so existing callers (and tests) are untouched.
func (s *ReadingService) WithHub(hub string, parser port.GraphExportParser) *ReadingService {
	s.hub = hub
	s.graphParser = parser
	return s
}

// WithProtocolRenderer sets the renderer for the protocol's own views, whose
// markdown escapes title punctuation that a document renderer may read as
// syntax (\( opens inline math). Unset, they share the document renderer.
func (s *ReadingService) WithProtocolRenderer(r port.Renderer) *ReadingService {
	s.protocol = r
	return s
}

// Read fetches a document from the world and renders it to sanitized HTML
// plus the margin's trust signals (ADR 0005 decisions 6–8): status, tags,
// provenance, and the document-properties block parsed from frontmatter.
func (s *ReadingService) Read(ctx context.Context, world, path string) (domain.Document, error) {
	epoch := s.fence.epoch()
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
		Title:       titleFor(raw.Path, raw.Metadata, rendered.Title),
		TitleAnchor: rendered.TitleAnchor,
		Source:      raw.Source,
		Path:        raw.Path,
		HTML:        rendered.HTML,
		Status:      resolveStatus(tags, rendered.Properties),
		Type:        raw.Metadata["type"],
		Tags:        tags,
		Properties:  rendered.Properties,
		Modified:    raw.Metadata["modified"],
		Version:     raw.Metadata["version"],
		Agent:       raw.Metadata["agent"],
		User:        raw.Metadata["user"],
		Meta:        otherMeta(raw.Metadata),
	}
	s.cachePut(epoch, docKey(ctx, world, path), doc)
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
		if doc, ok := s.cache.Get(docKey(ctx, world, path)); ok {
			return doc, nil
		}
	}
	return s.Read(ctx, world, path)
}

// TagCached is ReadCached for tag-page panes.
func (s *ReadingService) TagCached(ctx context.Context, world, tag string) (domain.Document, error) {
	if s.cache != nil {
		if doc, ok := s.cache.Get(tagKey(ctx, world, tag)); ok {
			return doc, nil
		}
	}
	return s.Tag(ctx, world, tag)
}

// cachePut stores doc unless a hint came since epoch: the read began before
// that write, so doc may be the edition the hint evicted.
func (s *ReadingService) cachePut(epoch uint64, key port.CacheKey, doc domain.Document) {
	if s.cache == nil {
		return
	}
	s.fence.mu.RLock()
	defer s.fence.mu.RUnlock()
	if s.fence.gen == epoch {
		s.cache.Put(key, doc)
	}
}

// Render kinds keep a document and a tag page from ever sharing a key.
const (
	kindDoc = "d"
	kindTag = "t"
)

func docKey(ctx context.Context, world, path string) port.CacheKey {
	return renderKey(ctx, kindDoc, world, path)
}

func tagKey(ctx context.Context, world, tag string) port.CacheKey {
	return renderKey(ctx, kindTag, world, tag)
}

func renderKey(ctx context.Context, kind, world, name string) port.CacheKey {
	return port.CacheKey{Kind: kind, Reader: port.ReaderKey(ctx), World: world, Name: name}
}

// Browse renders a directory listing (the stacks) at path.
func (s *ReadingService) Browse(ctx context.Context, world, path string) (domain.Document, error) {
	epoch := s.fence.epoch()
	raw, err := s.world.List(ctx, world, path)
	if err != nil {
		return domain.Document{}, err
	}
	doc, err := s.render(raw, "Index of "+path)
	if err != nil {
		return domain.Document{}, err
	}
	// A listing has no edition: its Version is a digest of what it lists, so
	// a live stream compares a listing as it does a document.
	sum := sha256.Sum256([]byte(raw.Body))
	doc.Version = hex.EncodeToString(sum[:8])
	s.cachePut(epoch, docKey(ctx, world, path), doc)
	return doc, nil
}

// BrowseCached is ReadCached for listing panes. Listing paths end in a
// slash and document paths never do, so they share the doc key space
// without collisions.
func (s *ReadingService) BrowseCached(ctx context.Context, world, path string) (domain.Document, error) {
	if s.cache != nil {
		if doc, ok := s.cache.Get(docKey(ctx, world, path)); ok {
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
	raw, err := s.world.Lookup(ctx, world, domain.LookupQuery{Scope: scope, Query: query})
	if err != nil {
		return domain.Document{}, err
	}
	return s.render(raw, "Catalog: "+query)
}

// Tag renders the card catalog filtered to one tag — the lookup-backed tag
// page (ADR 0005 decision 5). The tag rides as both query and filter: the
// filter does the exact narrowing, the query keeps ranking sensible.
func (s *ReadingService) Tag(ctx context.Context, world, tag string) (domain.Document, error) {
	epoch := s.fence.epoch()
	raw, err := s.world.Lookup(ctx, world, domain.LookupQuery{Scope: "/", Query: tag, Filter: "tag=" + tag})
	if err != nil {
		return domain.Document{}, err
	}
	doc, err := s.render(raw, "Tagged: "+tag)
	if err != nil {
		return domain.Document{}, err
	}
	s.cachePut(epoch, tagKey(ctx, world, tag), doc)
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
	rendered, err := s.protocol.Render(raw.Body)
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

// metaSurfaced are the out-of-band keys already shown in a dedicated margin
// slot — the OKF group (type, tags, modified→timestamp), provenance (version,
// agent, user), and the H1 (title). otherMeta excludes them so the catch-all metadata
// block surfaces everything else (importance, etag, content-hash, and any
// publisher-custom keys) without double-printing what the curated slots hold.
var metaSurfaced = map[string]bool{
	"title": true, "tags": true, "type": true,
	"modified": true, "version": true, "agent": true, "user": true,
}

// otherMeta returns every out-of-band metadata entry not already surfaced in a
// dedicated slot, sorted by key for a stable margin order. (status is split out
// at the protocol layer and never appears in the map.)
func otherMeta(m map[string]string) []domain.Property {
	keys := make([]string, 0, len(m))
	for k, v := range m {
		if metaSurfaced[k] || strings.TrimSpace(v) == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]domain.Property, 0, len(keys))
	for _, k := range keys {
		out = append(out, domain.Property{Key: k, Value: m[k]})
	}
	return out
}

func normalizeStatus(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if !statusValue.MatchString(v) {
		return "draft"
	}
	return v
}
