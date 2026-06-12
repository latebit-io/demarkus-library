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

// ReadingService implements port.ReadingService over a world gateway and a
// renderer.
type ReadingService struct {
	world    port.WorldGateway
	renderer port.Renderer
}

// compile-time check that ReadingService satisfies the inbound port.
var _ port.ReadingService = (*ReadingService)(nil)

// NewReadingService wires the outbound ports into the core.
func NewReadingService(world port.WorldGateway, renderer port.Renderer) *ReadingService {
	return &ReadingService{world: world, renderer: renderer}
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
	return domain.Document{
		Title:      titleFor(raw.Path, raw.Metadata),
		Source:     raw.Source,
		Path:       raw.Path,
		HTML:       rendered.HTML,
		Status:     resolveStatus(tags, rendered.Properties),
		Tags:       tags,
		Properties: rendered.Properties,
		Modified:   raw.Metadata["modified"],
		Version:    raw.Metadata["version"],
		Agent:      raw.Metadata["agent"],
	}, nil
}

// Browse renders a directory listing (the stacks) at path.
func (s *ReadingService) Browse(ctx context.Context, world, path string) (domain.Document, error) {
	raw, err := s.world.List(ctx, world, path)
	if err != nil {
		return domain.Document{}, err
	}
	return s.render(raw, "Index of "+path)
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
	return s.render(raw, "Tagged: "+tag)
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

// titleFor prefers the cataloged title, falling back to the path basename.
func titleFor(path string, meta map[string]string) string {
	if t := strings.TrimSpace(meta["title"]); t != "" {
		return t
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
