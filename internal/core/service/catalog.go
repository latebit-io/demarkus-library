package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// defaultCatalogHits caps a catalog search that names no limit: enough to
// rank, few enough to read.
const defaultCatalogHits = 25

// Lookup implements port.Catalog: a ranked LOOKUP over one world, or every
// readable world when q.World is empty, parsed into hits. A partial universe
// answer returns its hits alongside a *domain.PartialLookupError.
func (s *ReadingService) Lookup(ctx context.Context, q domain.CatalogQuery) (domain.CatalogResult, error) {
	lq := domain.LookupQuery{Scope: "/", Query: q.Query, Limit: q.Limit, Match: q.Match}
	if q.Tag != "" {
		lq.Filter = "tag=" + q.Tag
		if lq.Query == "" {
			lq.Query = q.Tag // the filter narrows; the query still ranks
		}
	}
	if lq.Query == "" {
		return domain.CatalogResult{}, errors.New("lookup: a query or a tag is required")
	}
	if lq.Limit <= 0 {
		lq.Limit = defaultCatalogHits
	}

	var raw domain.RawDocument
	var err error
	if q.World == "" {
		raw, err = s.world.LookupAll(ctx, lq)
	} else {
		raw, err = s.world.Lookup(ctx, q.World, lq)
	}
	if err != nil {
		return domain.CatalogResult{}, fmt.Errorf("lookup %q: %w", lq.Query, err)
	}

	fallbackWorld, qualifiedOnly := q.World, false
	if q.World == "" {
		fallbackWorld, qualifiedOnly = raw.Source, raw.Source == ""
	}
	result := domain.CatalogResult{BodyMatched: raw.Metadata["match"] == domain.MatchBody}
	rows := parseCatalogRows(raw.Body, fallbackWorld, qualifiedOnly, lq.Limit)
	for i := range rows {
		row := &rows[i]
		result.Hits = append(result.Hits, domain.CatalogHit{
			Ref:        domain.Ref{World: row.World, Path: row.Path},
			Anchor:     row.Anchor,
			Title:      row.Title,
			Tags:       row.Tags,
			Status:     row.Status,
			Importance: row.Importance,
			Snippet:    row.Snippet,
		})
	}
	return result, partialLookup(raw)
}

// Versions implements port.Catalog: the edition list as the world writes it.
func (s *ReadingService) Versions(ctx context.Context, world, path string) (domain.RawDocument, error) {
	raw, err := s.world.Versions(ctx, world, path)
	if err != nil {
		return domain.RawDocument{}, fmt.Errorf("versions %s%s: %w", world, path, err)
	}
	return raw, nil
}

// partialLookup reads a universe LOOKUP's partial status: nil when every world
// answered, else which failed, so absence there is never read as authoritative.
func partialLookup(raw domain.RawDocument) error {
	if raw.Metadata["status"] != "partial" {
		return nil
	}
	failed, _ := strconv.Atoi(raw.Metadata["failed"]) // counts are advisory; 0 on a malformed header
	worlds, _ := strconv.Atoi(raw.Metadata["worlds"])
	return &domain.PartialLookupError{
		Failed:       failed,
		Worlds:       worlds,
		FailedWorlds: parseLookupFailureWorlds(raw.Body),
	}
}
