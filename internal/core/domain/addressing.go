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
