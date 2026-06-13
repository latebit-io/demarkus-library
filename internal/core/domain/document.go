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

// Property is one parsed body-frontmatter key/value, in source order. The
// reading room renders these in the margin's document-properties block
// (ADR 0005 decision 7): the fence is stripped from the body but its content
// is shown friendly, never raw.
type Property struct {
	Key   string
	Value string
}

// Rendered is the renderer's output: sanitized HTML plus the properties
// parsed from the body's leading frontmatter fence, if any.
type Rendered struct {
	HTML       string
	Properties []Property
}

// Ref is a knowledge-graph coordinate: a document addressed by (world, path).
// It is comparable, so it serves as a map key in the link graph. Edges are
// observed at render time (rewriteLinks already resolves every link to its
// target world+path), giving backlinks and the graph pane a transport-
// symmetric source with no broker dependency (R3 decision; ADR 0005 §16).
type Ref struct {
	World string
	Path  string
}

// Neighborhood is the graph pane's data (ADR 0005 decision 4): one document
// and its observed edges — Out are documents Center links to, In are
// documents that link to Center. Derived from the render-time observed-links
// map, so a never-rendered neighbor is simply absent (honest cold state).
type Neighborhood struct {
	Center Ref
	Out    []Ref
	In     []Ref
}

// Edge is a directed link between two documents in the knowledge graph. The
// floor renders these between world clusters (and to portal nodes); they come
// from the durable hub graph export unioned with the R3 observed-links map.
type Edge struct {
	From Ref
	To   Ref
}

// WorldInfo is one world of the universe: a mark_worlds row in broker mode,
// or the home world in single-world QUIC mode. URL is the world's public
// mark:// address and may be empty — Name remains the addressing primitive.
type WorldInfo struct {
	Name string
	URL  string
}

// FloorDoc is one catalogued document rendered on the floor: a world's
// satellite, weighted by catalog importance, badged by status.
type FloorDoc struct {
	Path       string
	Title      string
	Importance float64
	Status     string
}

// FloorWorld is one world's cluster on the floor: its identity, its
// top-importance documents, and whether the catalog read failed (an
// unreachable world still renders — dimmed, satellite-less — rather than
// hiding; absence would read as nonexistence).
//
// Portal marks a world that is not in the authorized set but appears as the
// far end of an observed/hub edge — an externally-linked host, the extensional
// universe made visible (ADR 0005 §16, plans addendum). A portal renders as a
// small rim node with no satellites.
type FloorWorld struct {
	World  WorldInfo
	Docs   []FloorDoc
	Err    bool
	Portal bool
}

// Floor is the universe view's data: every visible world cluster plus the
// edges between them (ADR 0005 decision 4 — the floor is pane zero). Derived
// entirely from MCP-readable channels (decision 11): mark_worlds + per-world
// catalog lookups for nodes, the hub graph export ∪ the observed-links map for
// edges. Edges are world-level: a link from any doc in From to any doc in To.
type Floor struct {
	Worlds []FloorWorld
	Edges  []Edge
}

// Document is a rendered, display-ready document. HTML is already sanitized; the
// inbound web adapter is responsible for marking it safe for its template.
//
// The margin fields carry the trust signals of ADR 0005 decisions 6–8: Status
// resolved by authority order (metadata status: tag axis, then frontmatter,
// absent ⇒ draft), Tags from catalog metadata, provenance
// (Modified/Version/Agent) from response metadata, Properties from parsed
// frontmatter. Listings and catalog views leave them zero — an empty margin
// is correct.
type Document struct {
	Title  string
	Source string
	Path   string
	HTML   string

	Status     string // status vocabulary: draft | wip | accepted | archived
	Tags       []string
	Properties []Property
	Modified   string
	Version    string
	Agent      string
}
