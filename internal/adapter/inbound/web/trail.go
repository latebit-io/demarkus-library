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
// and trail chunks.
const (
	paneDoc = "d"
	paneTag = "tags"
)

// paneAddr addresses one pane: a document (or listing) in a world, or a
// world's tag page. Value is the doc path (leading slash) or the tag.
type paneAddr struct {
	Kind  string
	World string
	Value string
}

// trail is the parsed spatial state: the pane path and which pane holds the
// reader's attention (margin + live fetch).
type trail struct {
	Panes []paneAddr
	Focus int
}

var errBadTrail = errors.New("malformed trail")

// parseTrail decodes the wildcard remainder of /t/* plus the focus query
// param. Unknown kinds, empty chunks, or more than maxPanes panes are
// errors — the server never mints such URLs, so they are hand-built and get
// a 400, not a guess. An out-of-range focus is clamped: sharing a trail and
// then archiving panes off it shouldn't 400 the link.
func parseTrail(rest, focusParam string) (trail, error) {
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
	return t, nil
}

// parsePaneChunk decodes one <world>/<kind>/<value> chunk.
func parsePaneChunk(chunk string) (paneAddr, error) {
	world, rest, ok := strings.Cut(chunk, "/")
	if !ok || world == "" || world == trailSep {
		return paneAddr{}, errBadTrail
	}
	if dec, err := url.PathUnescape(world); err == nil {
		world = dec
	}
	kind, value, ok := strings.Cut(rest, "/")
	if !ok || value == "" {
		return paneAddr{}, errBadTrail
	}
	switch kind {
	case paneDoc:
		return paneAddr{Kind: paneDoc, World: world, Value: "/" + value}, nil
	case paneTag:
		if strings.Contains(value, "/") {
			return paneAddr{}, errBadTrail
		}
		if dec, err := url.PathUnescape(value); err == nil {
			value = dec
		}
		return paneAddr{Kind: paneTag, World: world, Value: value}, nil
	default:
		return paneAddr{}, errBadTrail
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

// paneChunk encodes one pane address as its chunk (no leading slash).
func paneChunk(p paneAddr) string {
	switch p.Kind {
	case paneTag:
		return url.PathEscape(p.World) + "/" + paneTag + "/" + url.PathEscape(p.Value)
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
			return trail{Panes: t.Panes, Focus: i}
		}
	}
	panes := append(slices.Clone(t.Panes[:idx+1]), target)
	if len(panes) > maxPanes {
		panes = panes[len(panes)-maxPanes:]
	}
	return trail{Panes: panes, Focus: len(panes) - 1}
}

// trailFocused is the spine/header click: same path, attention moves.
func trailFocused(t trail, idx int) trail {
	return trail{Panes: t.Panes, Focus: idx}
}
