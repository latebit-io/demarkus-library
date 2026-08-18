package domain

import "strings"

// IsListingPath reports whether a (world, path) addresses a directory listing
// (the stacks) rather than a document. The convention: a path ending in "/" is a
// listing, anything else is a document. This is the single definition of that
// addressing rule — the read dispatch (service.Open/OpenCached) and the web
// adapter's margin/edge-source presentation both consult it, so the two never
// drift.
func IsListingPath(path string) bool {
	return strings.HasSuffix(path, "/")
}

// IsVersionPath reports whether a path addresses a pinned edition of a
// document ("/plans/x.md/v12") rather than the document itself. A version is a
// snapshot, never an edge source: its links are the document's links at an
// earlier moment, so recording them would list every edition a reader happened
// to open as a separate "referenced by" entry.
func IsVersionPath(path string) bool {
	slash := strings.LastIndex(path, "/")
	if slash <= 0 {
		return false
	}
	seg, parent := path[slash+1:], path[:slash]
	if len(seg) < 2 || seg[0] != 'v' {
		return false
	}
	for _, r := range seg[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	// Only a markdown document has editions, so "/notes/v2" (a document named
	// v2) stays a document while "/notes.md/v2" is its edition.
	return strings.HasSuffix(parent, ".md")
}
