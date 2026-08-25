package service

import (
	"context"
	"strconv"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// Name-mode is a known-item switcher over catalog title, path, world, and status.
// Universe scope uses broker fan-out; world scope remains a direct lookup.

const nameIndexMax = 1000

// NameIndex assembles the palette's name-mode index. The single-world case
// (any scope but "universe") propagates a read failure so the web adapter can
// map it to an HTTP status — an outage must not look like "no matches".
//
// "universe" scope spans every authorized world through one broker request.
func (s *ReadingService) NameIndex(ctx context.Context, scope, world string) ([]domain.IndexEntry, error) {
	// The durable topology sources the orphan flag (ADR 0006 §0.2). Read once;
	// when no hub is configured it's empty and free, so the per-keystroke palette
	// pays nothing — only orphan-aware callers on hub systems incur the join.
	topo := s.readHub(ctx, s.hub)
	var host2name map[string]string
	if len(topo.nodes) > 0 {
		var err error
		if host2name, err = s.host2name(ctx); err != nil {
			return nil, err // outbound failure propagates; the web layer decides
		}
	}

	if scope != "universe" {
		return s.worldNameIndex(ctx, world, worldOrphans(world, host2name, topo))
	}

	raw, err := s.world.LookupAll(ctx, "/", "*", "", nameIndexMax)
	if err != nil {
		return nil, err
	}
	var partial error
	if raw.Metadata["status"] == "partial" {
		failed, _ := strconv.Atoi(raw.Metadata["failed"])
		worlds, _ := strconv.Atoi(raw.Metadata["worlds"])
		partial = &domain.PartialLookupError{
			Failed:       failed,
			Worlds:       worlds,
			FailedWorlds: parseLookupFailureWorlds(raw.Body),
		}
	}

	rows := parseCatalogRows(raw.Body, raw.Source, raw.Source == "", nameIndexMax)
	out := make([]domain.IndexEntry, 0, len(rows))
	orphans := make(map[string]map[string]bool)
	for _, row := range rows {
		if _, ok := orphans[row.World]; !ok {
			orphans[row.World] = worldOrphans(row.World, host2name, topo)
		}
		out = append(out, domain.IndexEntry{
			Title:  row.Title,
			Path:   row.Path,
			World:  row.World,
			Status: row.Status,
			Orphan: orphans[row.World][row.Path],
		})
	}
	return out, partial
}

// worldNameIndex returns one world's catalog as index entries, tagging each with
// orphan membership (orphans is the world's reference-orphan path set, possibly
// nil). A read failure returns the error (the caller decides whether to degrade
// or propagate); a canceled/timed-out context always propagates.
func (s *ReadingService) worldNameIndex(ctx context.Context, world string, orphans map[string]bool) ([]domain.IndexEntry, error) {
	raw, err := s.world.Lookup(ctx, world, "/", "*", "", nameIndexMax)
	if err != nil {
		return nil, err
	}
	docs := parseCatalogTable(raw.Body, nameIndexMax)
	out := make([]domain.IndexEntry, 0, len(docs))
	for _, d := range docs {
		out = append(out, domain.IndexEntry{
			Title:  d.Title,
			Path:   d.Path,
			World:  world,
			Status: d.Status,
			Orphan: orphans[d.Path],
		})
	}
	return out, nil
}
