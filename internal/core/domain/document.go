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

// ErrConflict means a write's expected_version no longer matches the world's
// current version — the document changed since the editor opened it (Phase 3
// cataloging desk). The reader reloads and reapplies rather than overwriting.
var ErrConflict = errors.New("document changed since it was opened")

// ErrWriteUnsupported means the gateway has no write path for this world — a
// direct QUIC world reached without a write token (Phase 3). Broker-mode worlds
// write through mark_publish; this keeps the read-only degradation honest.
var ErrWriteUnsupported = errors.New("writing not supported for this world")

// PublishMeta is the out-of-band metadata a write carries (ADR 0005 decision
// 11; the demarkus metadata channel). It maps to mark_publish's metadata object
// — never a body frontmatter fence. Status rides Tags as the status: axis, so
// it is not a separate field here. Importance is the raw 0–1 string (empty ⇒
// omitted) so the editor round-trips exactly what the catalog holds.
type PublishMeta struct {
	Title      string
	Tags       []string
	Importance string
}

// EditDraft is the source view the cataloging desk edits: a document's raw
// markdown body plus its current out-of-band metadata and version, fetched to
// pre-fill the edit form. Version guards the write (expected_version); Status is
// split out of Tags for the form's status picker.
type EditDraft struct {
	Path       string
	Body       string
	Title      string
	Tags       []string // ordinary tags, status: axis removed
	Importance string
	Status     string
	Version    int
}

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
//
// Address is the world's internal dial address (the broker's mark_worlds
// `address` column): the host:port the broker routes to and the federation
// agent crawls by, which is therefore how the topology graph keys this world's
// nodes. The floor uses it to join host-keyed graph edges back to Name. Empty
// for older brokers / QUIC mode, where URL is the join host instead.
type WorldInfo struct {
	Name    string
	URL     string
	Address string
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

// WorldCluster is one top-level directory of a world's catalog on the world
// map (zoom level 2; plans §"Universe-view research"). Dir is the first path
// segment (the heading "plans/"); Docs are the top-importance documents drawn
// as labeled nodes; More is how many further docs the dir holds beyond Docs —
// rendered as a "+N more" aggregate bubble that links to ListPath (the dir's
// listing pane, the stacks). Dir "" collects the world-root documents.
type WorldCluster struct {
	Dir      string
	Docs     []FloorDoc
	More     int
	ListPath string
}

// WorldMap is one world's catalog zoomed in (ADR 0005 decision 4 — the floor
// at one zoom in): its documents grouped into directory clusters, plus the
// intra-world edges among the rendered (labeled) documents. Derived from the
// same MCP-readable channel the floor uses (mark_lookup "*"), so the projection
// adds layout, never information.
type WorldMap struct {
	World    WorldInfo
	Clusters []WorldCluster
	Edges    []Edge
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
