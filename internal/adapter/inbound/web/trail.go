package web

import (
	"errors"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// The trail (ADR 0005 decisions 1–4): the reading room's spatial state is a
// single path of panes, root → focus, owned entirely by the URL. This file
// is the codec and the click algebra — pure functions, no I/O. The format is
// a public contract agents mint and parse (decision 14; spec published
// in-universe at /.well-known/library/trails.md):
//
//	/t/<pane>/~/<pane>/~/<pane>?focus=<i>
//
// where each pane chunk is the tail of the pane's standalone /w/ route —
// <world>/d/<path> for a document (trailing slash = the stacks) or
// <world>/tags/<tag> for a catalog tag page. `~` is a reserved separator
// segment; paths keep raw slashes (no %2F for ingress to mangle); focus is
// 0-based and defaults to the last pane.
const (
	// maxPanes caps trail depth (decision 1). An append past the cap drops
	// the oldest pane; a hand-minted URL past it is rejected.
	maxPanes = 10
	// trailSep is the reserved separator segment between pane chunks.
	trailSep = "~"
)

// Pane address kinds — the path segment that names them in both /w/ routes
// and trail chunks. paneFloor is the universe view (pane zero, ADR 0005
// decision 4): a single-segment chunk with no world — the floor IS the
// whole universe.
const (
	paneDoc = "d"
	paneTag = "tags"
	// paneFloor is the universe view (pane zero, ADR 0005 decision 4). The
	// bare "u" chunk (no world) is the whole-universe floor; "<world>/u/" is
	// that world's map — the same component one zoom in (the world-view zoom
	// level). World distinguishes the two.
	paneFloor = "u"
	// paneGraph is the graph neighborhood pane (R3; ADR 0005 decisions 4/5):
	// a document and its observed links/backlinks, rendered as SSR SVG, that
	// continues the trail. Same value rule as paneDoc — the chunk tail is
	// <world>/g/<path>.
	paneGraph = "g"
)

// paneAddr addresses one pane: a document (or listing) in a world, or a
// world's tag page. Value is the doc path (leading slash) or the tag.
type paneAddr struct {
	Kind  string
	World string
	Value string
}

// trail is the parsed spatial state: the pane path, which pane holds the
// reader's attention (margin + live fetch), and which pane (if any) is open
// in the reader overlay (R4).
type trail struct {
	Panes  []paneAddr
	Focus  int
	Reader int // pane index shown in the reader overlay; -1 = no overlay
}

var errBadTrail = errors.New("malformed trail")

// parseTrail decodes the wildcard remainder of /t/* plus the focus query
// param. Unknown kinds, empty chunks, or more than maxPanes panes are
// errors — the server never mints such URLs, so they are hand-built and get
// a 400, not a guess. An out-of-range focus is clamped: sharing a trail and
// then archiving panes off it shouldn't 400 the link.
func parseTrail(rest, focusParam, readerParam string) (trail, error) {
	var t trail
	for chunk := range strings.SplitSeq(rest, "/"+trailSep+"/") {
		addr, err := parsePaneChunk(chunk)
		if err != nil {
			return trail{}, err
		}
		t.Panes = append(t.Panes, addr)
	}
	if len(t.Panes) > maxPanes {
		return trail{}, errBadTrail
	}
	t.Focus = len(t.Panes) - 1
	if focusParam != "" {
		if i, err := strconv.Atoi(focusParam); err == nil {
			t.Focus = max(0, min(i, len(t.Panes)-1))
		}
	}
	// Reader overlay (R4): a presentation lens addressed by ?reader=<paneIndex>.
	// It reuses the focused pane's already-fetched document, so a valid reader
	// index also takes Focus — the single-live-read invariant (ADR 0005 d9)
	// holds and the overlay always has the focused pane's full margin. Only
	// prose panes (doc/listing/tag) overlay; a floor/graph pane is just a
	// bigger SVG, deferred past v1. Out-of-range, non-prose, or junk ⇒ no
	// overlay (-1), never a 400 — a stale shared link degrades to the canvas.
	t.Reader = -1
	if readerParam != "" {
		if i, err := strconv.Atoi(readerParam); err == nil && i >= 0 && i < len(t.Panes) {
			if k := t.Panes[i].Kind; k == paneDoc || k == paneTag {
				t.Reader = i
				t.Focus = i
			}
		}
	}
	return t, nil
}

// parsePaneChunk decodes one <world>/<kind>/<value> chunk, or the bare
// "u" floor chunk.
func parsePaneChunk(chunk string) (paneAddr, error) {
	if chunk == paneFloor {
		return paneAddr{Kind: paneFloor}, nil
	}
	world, rest, ok := strings.Cut(chunk, "/")
	if !ok || world == "" || world == trailSep {
		return paneAddr{}, errBadTrail
	}
	if dec, err := url.PathUnescape(world); err == nil {
		world = dec
	}
	// The "/" after the kind must be present (real chunks are always
	// "<kind>/<value>"); paneAddrFromParts owns the per-kind value rules.
	kind, value, ok := strings.Cut(rest, "/")
	if !ok {
		return paneAddr{}, errBadTrail
	}
	addr, ok := paneAddrFromParts(world, kind, value)
	if !ok {
		return paneAddr{}, errBadTrail
	}
	return addr, nil
}

// paneAddrFromParts builds a pane address from an already-unescaped world, a
// kind segment, and the raw value segment after it. This is the single
// source of truth for what a doc vs tag chunk means, shared by both the
// trail-chunk parser (/t/...) and the route decoder (/w/...) so the two
// can't drift — they diverged once on the world-root listing, which is the
// bug this consolidation removes. Returns false for an unknown kind or an
// invalid tag.
func paneAddrFromParts(world, kind, value string) (paneAddr, bool) {
	switch kind {
	case paneFloor:
		// "<world>/u/" — a world map (the floor one zoom in). The world IS the
		// address, so the value must be empty (spec: trailing slash, no value);
		// a non-empty "<world>/u/junk" is malformed and rejected like any other
		// bad chunk, not silently ignored. The bare "u" floor never reaches here
		// (parsePaneChunk short-circuits it before any world split).
		if value != "" {
			return paneAddr{}, false
		}
		return paneAddr{Kind: paneFloor, World: world}, true
	case paneDoc, paneGraph:
		// An empty value is the world-root listing: "<world>/d/" means
		// path "/" (the stacks), which the floor's world node and any link
		// to a world root produce. Value carries the leading slash, "" → "/".
		// Doc paths keep raw slashes (no %2F), so they are not unescaped. The
		// graph pane shares the rule — its value is the same document path.
		return paneAddr{Kind: kind, World: world, Value: "/" + value}, true
	case paneTag:
		// A tag is a single, PathEscape'd, non-empty segment. Validate the
		// DECODED value: a raw "foo%2Fbar" has no literal slash but unescapes
		// to "foo/bar", which would break the single-segment invariant — so
		// the empty/slash checks must run after unescaping.
		if dec, err := url.PathUnescape(value); err == nil {
			value = dec
		}
		if value == "" || strings.Contains(value, "/") {
			return paneAddr{}, false
		}
		return paneAddr{Kind: paneTag, World: world, Value: value}, true
	default:
		return paneAddr{}, false
	}
}

// trailURL encodes the trail back to its URL. focus is omitted when it is
// the default (last pane) so plain append-clicks share the canonical form.
func trailURL(t trail) string {
	chunks := make([]string, len(t.Panes))
	for i, p := range t.Panes {
		chunks[i] = paneChunk(p)
	}
	u := "/t/" + strings.Join(chunks, "/"+trailSep+"/")
	if t.Focus != len(t.Panes)-1 {
		u += "?focus=" + strconv.Itoa(t.Focus)
	}
	return u
}

// trailReaderURL encodes the trail with the reader overlay open on pane
// `reader` (0-based); reader < 0 yields the bare trail (the close URL). The
// reader param implies attention on that pane — parseTrail re-derives Focus
// from it — so focus is not encoded separately. This is the only URL builder
// that emits ?reader=; trailURL stays reader-free (the canonical canvas URL),
// so every existing click closes the overlay.
func trailReaderURL(t trail, reader int) string {
	chunks := make([]string, len(t.Panes))
	for i, p := range t.Panes {
		chunks[i] = paneChunk(p)
	}
	u := "/t/" + strings.Join(chunks, "/"+trailSep+"/")
	if reader >= 0 {
		u += "?reader=" + strconv.Itoa(reader)
	}
	return u
}

// paneChunk encodes one pane address as its chunk (no leading slash).
func paneChunk(p paneAddr) string {
	switch p.Kind {
	case paneFloor:
		if p.World == "" {
			return paneFloor
		}
		return url.PathEscape(p.World) + "/" + paneFloor + "/"
	case paneTag:
		return url.PathEscape(p.World) + "/" + paneTag + "/" + url.PathEscape(p.Value)
	case paneGraph:
		return url.PathEscape(p.World) + "/" + paneGraph + p.Value
	default:
		return url.PathEscape(p.World) + "/" + paneDoc + p.Value
	}
}

// trailAfterClick is the click algebra (decision 2): a link in pane idx
// truncates everything right of it and appends the target, focusing it — a
// target already on the path is focused instead, never duplicated. An
// append past maxPanes drops the oldest pane.
func trailAfterClick(t trail, idx int, target paneAddr) trail {
	for i, p := range t.Panes {
		if p == target {
			return trail{Panes: t.Panes, Focus: i, Reader: -1}
		}
	}
	panes := append(slices.Clone(t.Panes[:idx+1]), target)
	if len(panes) > maxPanes {
		panes = panes[len(panes)-maxPanes:]
	}
	return trail{Panes: panes, Focus: len(panes) - 1, Reader: -1}
}

// trailFocused is the spine/header click: same path, attention moves.
func trailFocused(t trail, idx int) trail {
	return trail{Panes: t.Panes, Focus: idx, Reader: -1}
}
