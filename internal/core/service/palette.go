package service

import (
	"context"
	"errors"
	"time"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// The name index is the catalog a reader jumps by: one world's, or every
// authorized world's through one broker request.

const nameIndexMax = 1000

// partialTTL is how long a partial universe index stands: a world that is
// down costs one fan-out per window, not one per keystroke.
const partialTTL = 5 * time.Second

// nameIndex is a cached name index. partial is the universe read's
// PartialLookupError, kept so a repeat still says which worlds are missing.
type nameIndex struct {
	entries []domain.IndexEntry
	partial error
}

// NameIndexCached serves a name index within the TTL, reading live on a miss
// (ADR 0009, ADR 0005 decision 9). A partial universe index is kept for
// partialTTL only and comes back with its error each time.
func (s *ReadingService) NameIndexCached(ctx context.Context, world string) ([]domain.IndexEntry, error) {
	if index, ok := s.names.getFresh(ctx, world, partialTTL); ok {
		return index.entries, index.partial
	}
	if index, ok := s.names.getFresh(ctx, world, floorTTL); ok && index.partial == nil {
		return index.entries, nil
	}
	epoch := s.names.epoch(world)
	entries, err := s.NameIndex(ctx, world)
	if _, partial := errors.AsType[*domain.PartialLookupError](err); err != nil && !partial {
		return entries, err
	}
	s.names.put(ctx, world, epoch, nameIndex{entries: entries, partial: err})
	return entries, err
}

// NameIndex assembles the name index of world, or of every authorized world
// when world is "". A read failure propagates, so an outage never looks like
// "no matches".
func (s *ReadingService) NameIndex(ctx context.Context, world string) ([]domain.IndexEntry, error) {
	if world != universe {
		// The durable topology sources the orphan flag (ADR 0006 §0.2):
		// best-effort, so a hub outage costs the flag, not the index.
		hosts, _ := s.readerHosts(ctx)
		return s.worldNameIndex(ctx, world, worldOrphans(world, hosts.host2name, s.hubGraph(ctx).topo))
	}
	raw, err := s.world.LookupAll(ctx, domain.LookupQuery{Scope: "/", Query: "*", Limit: nameIndexMax})
	if err != nil {
		return nil, err
	}
	rows := parseCatalogRows(raw.Body, raw.Source, raw.Source == "", nameIndexMax)
	out := make([]domain.IndexEntry, 0, len(rows))
	for i := range rows {
		// No orphan flag: only a world's own listing shows it.
		out = append(out, indexEntry(&rows[i], rows[i].World, nil))
	}
	return out, partialLookup(raw)
}

// worldNameIndex returns one world's catalog as index entries; orphans is the
// world's reference-orphan path set, possibly nil.
func (s *ReadingService) worldNameIndex(ctx context.Context, world string, orphans map[string]bool) ([]domain.IndexEntry, error) {
	raw, err := s.world.Lookup(ctx, world, domain.LookupQuery{Scope: "/", Query: "*", Limit: nameIndexMax})
	if err != nil {
		return nil, err
	}
	rows := parseCatalogRows(raw.Body, world, false, nameIndexMax)
	out := make([]domain.IndexEntry, 0, len(rows))
	for i := range rows {
		out = append(out, indexEntry(&rows[i], world, orphans))
	}
	return out, nil
}

// indexEntry is a catalog row as the palette reads it; orphans is the row's
// world's reference-orphan path set, possibly nil.
func indexEntry(row *catalogRow, world string, orphans map[string]bool) domain.IndexEntry {
	return domain.IndexEntry{
		Title:  row.Title,
		Path:   row.Path,
		World:  world,
		Status: row.Status,
		Tags:   row.Tags,
		Orphan: orphans[row.Path],
	}
}
