package service

import (
	"context"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// The name index is the catalog a reader jumps by: one world's, or every
// authorized world's through one broker request.

const nameIndexMax = 1000

// NameIndexCached serves a name index within the TTL, reading live on a miss
// (ADR 0009, ADR 0005 decision 9). A partial universe index comes back with
// its error and is not kept.
func (s *ReadingService) NameIndexCached(ctx context.Context, world string) ([]domain.IndexEntry, error) {
	if entries, ok := s.names.getFresh(ctx, world, floorTTL); ok {
		return entries, nil
	}
	epoch := s.names.epoch(world)
	entries, err := s.NameIndex(ctx, world)
	if err != nil {
		return entries, err
	}
	s.names.put(ctx, world, epoch, entries)
	return entries, nil
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
