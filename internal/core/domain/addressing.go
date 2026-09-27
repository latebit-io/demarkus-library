package domain

import (
	"net/url"
	"path"
	"strings"
)

// IsListingPath reports whether a (world, path) addresses a directory listing
// (the stacks) rather than a document. The convention: a path ending in "/" is a
// listing, anything else is a document. This is the single definition of that
// addressing rule — the read dispatch (service.Open/OpenCached) and the web
// adapter's margin/edge-source presentation both consult it, so the two never
// drift.
func IsListingPath(docPath string) bool {
	return strings.HasSuffix(docPath, "/")
}

// IsVersionPath reports whether a path addresses a pinned edition of a
// document ("/plans/x.md/v12") rather than the document itself. A version is a
// snapshot, never an edge source: its links are the document's links at an
// earlier moment, so recording them would list every edition a reader happened
// to open as a separate "referenced by" entry.
func IsVersionPath(docPath string) bool {
	slash := strings.LastIndex(docPath, "/")
	if slash <= 0 {
		return false
	}
	seg, parent := docPath[slash+1:], docPath[:slash]
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

// LinkTarget is where one document link points inside the universe. Fragment
// is the in-document anchor, if any.
type LinkTarget struct {
	Ref
	Fragment string
}

// ResolveHref resolves a link written in the document at (world, basePath):
// a mark:// URL crosses to its world, an absolute path stays in world, and a
// relative path joins the document's directory. ok is false for external
// schemes, in-page anchors, and unparseable hrefs. A trailing slash survives,
// so a listing target stays a listing (IsListingPath).
func ResolveHref(href, world, basePath string) (LinkTarget, bool) {
	if href == "" || strings.HasPrefix(href, "#") {
		return LinkTarget{}, false
	}
	// VERSIONS emits percent-encoded paths (e.g. %2Fdoc.md/v2); decode first.
	if dec, err := url.PathUnescape(href); err == nil {
		href = dec
	}
	u, err := url.Parse(href)
	if err != nil || (u.Scheme != "" && u.Scheme != "mark") {
		return LinkTarget{}, false
	}

	targetWorld := world
	worldPath := u.Path
	hadTrailingSlash := strings.HasSuffix(worldPath, "/")
	switch {
	case u.Scheme == "mark":
		// The authority IS the target world: a knowledge-system name, or a
		// host[:port] (u.Host carries the port when present).
		if u.Host != "" {
			targetWorld = u.Host
		}
		if worldPath == "" {
			worldPath = "/"
			hadTrailingSlash = true
		}
	case strings.HasPrefix(worldPath, "/"):
	default:
		worldPath = path.Join(path.Dir(basePath), worldPath)
	}

	worldPath = path.Clean(worldPath)
	if worldPath == "." || worldPath == "/" {
		worldPath = "/"
	} else if hadTrailingSlash {
		worldPath += "/"
	}
	return LinkTarget{Ref: Ref{World: targetWorld, Path: worldPath}, Fragment: u.Fragment}, true
}
