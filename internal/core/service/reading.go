// Package service contains the application core — the hexagon. It implements the
// inbound ports using only outbound ports, so it has no knowledge of Echo, QUIC,
// or goldmark. This is where the "librarian" intelligence lives.
package service

import (
	"context"
	"strings"

	"github.com/latebit/demarkus-library/internal/core/domain"
	"github.com/latebit/demarkus-library/internal/core/port"
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

// Read fetches a document from the world and renders it to sanitized HTML.
func (s *ReadingService) Read(ctx context.Context, path string) (domain.Document, error) {
	raw, err := s.world.Fetch(ctx, path)
	if err != nil {
		return domain.Document{}, err
	}
	return s.render(raw, titleFor(raw.Path, raw.Metadata))
}

// Browse renders a directory listing (the stacks) at path.
func (s *ReadingService) Browse(ctx context.Context, path string) (domain.Document, error) {
	raw, err := s.world.List(ctx, path)
	if err != nil {
		return domain.Document{}, err
	}
	return s.render(raw, "Index of "+path)
}

// History renders the edition history of the document at path.
func (s *ReadingService) History(ctx context.Context, path string) (domain.Document, error) {
	raw, err := s.world.Versions(ctx, path)
	if err != nil {
		return domain.Document{}, err
	}
	return s.render(raw, "Editions of "+path)
}

// Search renders the card catalog (LOOKUP) results for query under scope.
func (s *ReadingService) Search(ctx context.Context, scope, query string) (domain.Document, error) {
	raw, err := s.world.Lookup(ctx, scope, query)
	if err != nil {
		return domain.Document{}, err
	}
	return s.render(raw, "Catalog: "+query)
}

// render turns a raw markdown document into a display-ready Document.
func (s *ReadingService) render(raw domain.RawDocument, title string) (domain.Document, error) {
	html, err := s.renderer.Render(raw.Body)
	if err != nil {
		return domain.Document{}, err
	}
	return domain.Document{
		Title:  title,
		Source: raw.Source,
		Path:   raw.Path,
		HTML:   html,
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
