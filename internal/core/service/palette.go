package service

import (
	"context"
	"errors"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// The command palette's name-mode index (ADR 0006 §3). Name-mode is a known-item
// switcher: the server supplies the catalog (title/path/world/status), the client
// fuzzy-matches it. This reuses the same MCP-readable channel the floor and world
// map use — mark_lookup "*", importance order — so the index adds no new protocol
// surface (content-mode/full-text is the separate SEARCH path, ADR 0006 §0.3).

// nameIndexMaxPerWorld caps a single world's contribution to the index — a
// switcher wants reach, not the whole long tail; the catalog's importance order
// means the cap keeps the most findable docs.
const nameIndexMaxPerWorld = 1000

// NameIndex assembles the palette's name-mode index. The single-world case
// (any scope but "universe") propagates a read failure so the web adapter can
// map it to an HTTP status — an outage must not look like "no matches".
//
// "universe" scope spans every authorized world and is best-effort across them:
// one world whose catalog won't read drops out of reach rather than failing the
// whole index. Cancellation/timeout always propagates — a terminated request
// must not render a half-index.
func (s *ReadingService) NameIndex(ctx context.Context, scope, world string) ([]domain.IndexEntry, error) {
	if scope != "universe" {
		return s.worldNameIndex(ctx, world)
	}

	var worlds []string
	ws, err := s.world.Worlds(ctx)
	switch {
	case err == nil:
		for _, w := range ws {
			worlds = append(worlds, w.Name)
		}
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return nil, err
	default:
		worlds = []string{world} // no world list ⇒ degrade to the reader's world
	}

	var out []domain.IndexEntry
	for _, w := range worlds {
		entries, err := s.worldNameIndex(ctx, w)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			continue // a world whose catalog won't read drops out of reach
		}
		out = append(out, entries...)
	}
	return out, nil
}

// worldNameIndex returns one world's catalog as palette entries. A read failure
// returns the error (the caller decides whether to degrade or propagate); a
// canceled/timed-out context always propagates.
func (s *ReadingService) worldNameIndex(ctx context.Context, world string) ([]domain.IndexEntry, error) {
	raw, err := s.world.Lookup(ctx, world, "/", "*", "", nameIndexMaxPerWorld)
	if err != nil {
		return nil, err
	}
	docs := parseCatalogTable(raw.Body, nameIndexMaxPerWorld)
	out := make([]domain.IndexEntry, 0, len(docs))
	for _, d := range docs {
		out = append(out, domain.IndexEntry{
			Title:  d.Title,
			Path:   d.Path,
			World:  world,
			Status: d.Status,
		})
	}
	return out, nil
}
