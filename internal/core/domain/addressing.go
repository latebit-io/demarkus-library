package domain

import (
	"net"
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

// IsHostShaped reports whether a world identifier addresses a server directly
// (host or host:port) rather than naming a knowledge-system world. Dots and
// colons never appear in broker world names, plus bare "localhost".
func IsHostShaped(worldID string) bool {
	return worldID == "localhost" || strings.ContainsAny(worldID, ".:")
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
	// u.Path is decoded, so VERSIONS' %2Fdoc.md/v2 resolves while an escaped
	// %23 or %3F stays part of the file name.
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

// DefaultHostPort is the demarkus protocol's default QUIC port (protocol.DefaultPort).
// A mark:// URL may omit it, so mark://soul.demarkus.io and the world named
// soul.demarkus.io:6309 name the same host.
const DefaultHostPort = "6309"

// HostKey is a host in its explicit-port form, so joins on hosts are port-stable;
// an explicit non-default port (127.0.0.1:6401) is kept, so distinct dev worlds
// on one host stay distinct.
func HostKey(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if h == "" {
		return h
	}
	// Already host:port (incl. bracketed IPv6 with a port) — leave it.
	if _, _, err := net.SplitHostPort(h); err == nil {
		return h
	}
	// A bare IPv6 literal has ≥2 colons and no brackets — its colons are the
	// address, not a port, so bracket it before appending the default port.
	if strings.Count(h, ":") >= 2 && !strings.HasPrefix(h, "[") {
		return "[" + h + "]:" + DefaultHostPort
	}
	return h + ":" + DefaultHostPort
}

// WorldKey is the one spelling of a world identifier: a host gets HostKey's
// explicit port, so "h" and "h:6309" (both appear in links) match, while a
// world name is kept as is.
func WorldKey(worldID string) string {
	if !IsHostShaped(worldID) {
		return worldID
	}
	return HostKey(worldID)
}
