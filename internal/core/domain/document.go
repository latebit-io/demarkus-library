// Package domain holds the reading-room entities and errors. It is the center
// of the hexagon: it depends on nothing else in the project (no Echo, no
// demarkus client, no html/template).
package domain

import "errors"

// ErrNotFound means a world has no document at the requested path (demarkus
// status not-found or archived).
var ErrNotFound = errors.New("document not found")

// ErrUnauthorized means a world rejected the read (status unauthorized or
// not-permitted). Phase 1 turns this into an OAuth challenge.
var ErrUnauthorized = errors.New("not authorized to read document")

// RawDocument is a document as fetched from a world, before rendering.
type RawDocument struct {
	Source   string            // world identity, e.g. host:port
	Path     string            // requested path
	Body     string            // raw markdown
	Metadata map[string]string // cataloged metadata (title, tags, importance, ...)
}

// Document is a rendered, display-ready document. HTML is already sanitized; the
// inbound web adapter is responsible for marking it safe for its template.
type Document struct {
	Title  string
	Source string
	Path   string
	HTML   string
}
