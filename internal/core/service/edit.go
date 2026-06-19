package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// The cataloging desk's write use cases (Phase 3; plans/phase-3-cataloging-desk.md).
// The library stays a projection of the demarkus write protocol: EditDraft reads
// the source to pre-fill the form, Preview renders the edit buffer with the same
// renderer the reader uses, and Publish writes then re-reads live so the room
// shows fresh content immediately (focused-live policy).

// EditDraft fetches the source view for the edit form: the raw markdown plus the
// current out-of-band metadata and version. Metadata is split for the form — the
// status: axis becomes its own field so the picker round-trips the badge.
func (s *ReadingService) EditDraft(ctx context.Context, world, path string) (domain.EditDraft, error) {
	raw, err := s.world.Fetch(ctx, world, path)
	if err != nil {
		return domain.EditDraft{}, err
	}
	ordinary, status := splitStatusAxis(splitTags(raw.Metadata["tags"]))
	// A fetched existing document always carries a version; if it can't be read
	// we refuse the draft rather than silently using 0 (the create sentinel),
	// which would bypass the conflict guard on save (don't swallow the error).
	version, err := strconv.Atoi(strings.TrimSpace(raw.Metadata["version"]))
	if err != nil {
		return domain.EditDraft{}, fmt.Errorf("edit draft %q: unreadable version metadata %q: %w", path, raw.Metadata["version"], err)
	}
	return domain.EditDraft{
		Path:       raw.Path,
		Body:       raw.Body,
		Title:      raw.Metadata["title"],
		Tags:       ordinary,
		Importance: strings.TrimSpace(raw.Metadata["importance"]),
		Status:     status,
		Version:    version,
	}, nil
}

// Preview renders edit-buffer markdown to sanitized HTML — the desk's live
// preview. Same renderer as the reader, so what you see is what publishes.
func (s *ReadingService) Preview(markdown string) (domain.Rendered, error) {
	return s.renderer.Render(markdown)
}

// Publish writes the document. On a clean write it re-reads live (refreshing
// the cache, focused-live) and returns the Document with a nil candidate.
// expectedVersion guards the write; a stale non-zero version yields a
// *domain.MergeCandidate (nothing written) for the desk to review and
// re-publish at its PublishAtVersion. A create (version 0) hitting an existing
// path is domain.ErrConflict, not a candidate (the gateway picks on_conflict by
// the version).
func (s *ReadingService) Publish(ctx context.Context, world, path, body string, meta domain.PublishMeta, expectedVersion int) (domain.Document, *domain.MergeCandidate, error) {
	res, err := s.world.Publish(ctx, world, path, body, meta, expectedVersion)
	if err != nil {
		return domain.Document{}, nil, err
	}
	if res.Merge != nil {
		return domain.Document{}, res.Merge, nil
	}
	s.invalidateTopology(world)
	doc, err := s.Read(ctx, world, path)
	return doc, nil, err
}

// invalidateTopology drops the cached floor and the world's cached map after a
// write, so a just-published or appended document shows on the universe view and
// the world map immediately instead of at the next cache-TTL rebuild. The
// rendered-document LRU is refreshed by the live re-read; the link graph
// re-records when the document next renders.
func (s *ReadingService) invalidateTopology(world string) {
	s.floor.invalidate()
	s.worldMaps.invalidate(world)
}

// Append adds body to the end of the document then re-reads it live (refreshing
// the cache). The lightweight "add to" — metadata is preserved and the version
// auto-resolves, so it carries no PublishMeta.
func (s *ReadingService) Append(ctx context.Context, world, path, body string) (domain.Document, error) {
	if _, err := s.world.Append(ctx, world, path, body); err != nil {
		return domain.Document{}, err
	}
	s.invalidateTopology(world)
	return s.Read(ctx, world, path)
}

// splitStatusAxis separates the status: axis tag (the trust-signal badge) from
// the ordinary tags — the form edits them as distinct controls (ADR 0005
// decision 7; status is a picker, not a free tag).
func splitStatusAxis(tags []string) (ordinary []string, status string) {
	for _, t := range tags {
		if v, ok := strings.CutPrefix(t, "status:"); ok {
			status = v
			continue
		}
		ordinary = append(ordinary, t)
	}
	return ordinary, status
}
