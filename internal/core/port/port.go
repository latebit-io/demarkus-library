// Package port declares the hexagon's boundaries: the interfaces the core
// offers (inbound / driving) and the interfaces the core needs (outbound /
// driven). Adapters depend on these; the core never depends on adapters.
package port

import (
	"context"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// ReadingService is the inbound (driving) port — the use cases the reading room
// exposes to its primary adapters (the web adapter). Driving adapters depend on
// this interface, not on the concrete service.
//
// Every method takes the request context (cancellation + the logged-in
// reader's bearer in broker mode, Phase 1b/ADR 0004) and a world: the library
// spans a universe of worlds, and a document's address is (world, path). A
// world is either a knowledge-system world name (resolved by the broker) or a
// demarkus host[:port] reached directly — the distributed knowledge graph is
// traversable across both.
type ReadingService interface {
	// Read fetches and renders the document at (world, path).
	Read(ctx context.Context, world, path string) (domain.Document, error)
	// Browse renders a directory listing (the stacks) at (world, path).
	Browse(ctx context.Context, world, path string) (domain.Document, error)
	// History renders the edition history of the document at (world, path).
	History(ctx context.Context, world, path string) (domain.Document, error)
	// Search renders the card catalog (LOOKUP) results for query under scope
	// in world.
	Search(ctx context.Context, world, scope, query string) (domain.Document, error)
	// Tag renders the card catalog filtered to one tag — the lookup-backed
	// tag page that replaces the global search box (ADR 0005 decision 5).
	Tag(ctx context.Context, world, tag string) (domain.Document, error)
	// Raw returns the unrendered source of the document at (world, path) —
	// the projection's escape to protocol (ADR 0005 decision 12).
	Raw(ctx context.Context, world, path string) (domain.RawDocument, error)

	// ReadCached, BrowseCached, and TagCached are the trail engine's
	// unfocused-pane reads (ADR 0005 decision 9): served from the
	// rendered-document cache, reading through on a miss. The focused pane
	// uses the live methods, which refresh the cache — so a trail click
	// costs exactly one world read. Without a cache wired they behave as
	// their live counterparts.
	ReadCached(ctx context.Context, world, path string) (domain.Document, error)
	BrowseCached(ctx context.Context, world, path string) (domain.Document, error)
	TagCached(ctx context.Context, world, tag string) (domain.Document, error)

	// RecordLinks records the in-universe document links observed in the
	// rendered document at (world, path), replacing any prior observation
	// (R3; ADR 0005 §16). The web adapter calls this after resolving links
	// (rewriteLinks owns the URL scheme); the core owns the edge store. This
	// is the render-time observed-links map that feeds Backlinks and
	// Neighborhood — transport-symmetric, no broker graph store required.
	RecordLinks(world, path string, targets []domain.Ref)
	// Backlinks returns the documents observed linking to (world, path) — the
	// margin's "referenced by" block and the graph pane's inbound edges.
	// Best-effort: empty when nothing has been observed linking here yet (the
	// correct cold state, ADR 0005 decision 8), never an error.
	Backlinks(world, path string) []domain.Ref
	// Neighborhood assembles the graph pane's data (ADR 0005 decision 4): the
	// document plus its observed outbound and inbound edges. Store-only (zero
	// world reads), so it works cold in both transports.
	Neighborhood(world, path string) domain.Neighborhood

	// Floor assembles the universe view's data (ADR 0005 decision 4):
	// the authorized worlds and each world's top-importance catalog
	// entries. Live rebuild; FloorCached serves the last build when the
	// floor pane is unfocused (the same focused-live policy as documents).
	Floor(ctx context.Context) (domain.Floor, error)
	FloorCached(ctx context.Context) (domain.Floor, error)

	// WorldMap assembles the world-view zoom (ADR 0005 decision 4 — the floor
	// one zoom in): one world's catalog grouped into directory clusters with
	// the intra-world edges among the rendered documents. Live rebuild;
	// WorldMapCached serves the last build for an unfocused/parent pane (the
	// focused-live policy every pane follows).
	WorldMap(ctx context.Context, world string) (domain.WorldMap, error)
	WorldMapCached(ctx context.Context, world string) (domain.WorldMap, error)

	// EditDraft fetches the source view for the cataloging desk's edit form
	// (Phase 3): the document's raw markdown plus its current metadata and
	// version, so the editor pre-fills exactly what the catalog holds.
	EditDraft(ctx context.Context, world, path string) (domain.EditDraft, error)
	// Preview renders edit-buffer markdown to sanitized HTML for the desk's
	// live preview — the same renderer the reader uses, so what you see is what
	// publishes. No fetch, no write.
	Preview(markdown string) (domain.Rendered, error)
	// Publish writes the document at (world, path). On a clean write it re-reads
	// live (focused-live: refreshes the cache) and returns the display-ready
	// Document with a nil candidate. expectedVersion guards the write: 0 creates
	// (a path-taken conflict is domain.ErrConflict); a non-zero stale version
	// returns a *domain.MergeCandidate (nothing committed) for the desk to review
	// and re-publish at its PublishAtVersion.
	Publish(ctx context.Context, world, path, body string, meta domain.PublishMeta, expectedVersion int) (domain.Document, *domain.MergeCandidate, error)
	// Append adds body to the end of the document at (world, path) and returns
	// the re-read result (focused-live). The lightweight "add to" — metadata is
	// preserved, the version auto-resolves.
	Append(ctx context.Context, world, path, body string) (domain.Document, error)
}

// WorldGateway is an outbound (driven) port — read from demarkus worlds. The
// adapter translates transport status into domain errors and returns markdown
// bodies for the core to render. Implementations: direct QUIC (world is a
// host), the broker MCP gateway (world is a knowledge-system name, bearer
// from ctx), and the federated composite that routes between them by the
// world identifier's shape.
type WorldGateway interface {
	Fetch(ctx context.Context, world, path string) (domain.RawDocument, error)
	List(ctx context.Context, world, path string) (domain.RawDocument, error)
	Versions(ctx context.Context, world, path string) (domain.RawDocument, error)
	// Lookup queries the catalog for query under scope. filter is the
	// catalog's comma-separated key=value predicate string ("" for none) —
	// tag pages use tag=<tag>. The query "*" is the match-all form (whole
	// catalog under scope, importance order) on servers ≥ the match-all
	// release; older servers reject it.
	Lookup(ctx context.Context, world, scope, query, filter string) (domain.RawDocument, error)
	// Worlds enumerates the universe this gateway can reach: the broker's
	// mark_worlds (authorization-filtered) or the single home world over
	// QUIC. The non-brokered universe is extensional — one world — so an
	// empty or single-element answer is correct there, not degraded.
	Worlds(ctx context.Context) ([]domain.WorldInfo, error)

	// Publish writes (creates or updates) the document at (world, path) — the
	// cataloging desk's write path (Phase 3), over mark_publish in broker mode.
	// body is pure markdown; meta is the out-of-band metadata (never a body
	// fence). expectedVersion guards the write (0 to create). The result is
	// either a committed Version or a Merge candidate: a create (version 0) that
	// hits an existing path is domain.ErrConflict, while a stale non-zero version
	// returns a PublishResult with Merge set (the broker three-way-merged rather
	// than failing). Gateways with no write path (direct QUIC, no write token)
	// return domain.ErrWriteUnsupported.
	Publish(ctx context.Context, world, path, body string, meta domain.PublishMeta, expectedVersion int) (domain.PublishResult, error)

	// Append concatenates body onto the end of the document at (world, path) —
	// the cataloging desk's lightweight "add to" (Phase 3), over mark_append.
	// Additive and metadata-preserving (the server auto-resolves the version),
	// so it carries no PublishMeta. Returns the new version; gateways with no
	// write path return domain.ErrWriteUnsupported.
	Append(ctx context.Context, world, path, body string) (int, error)
}

// Renderer is an outbound (driven) port — markdown to sanitized HTML plus
// the properties parsed from a leading frontmatter fence.
type Renderer interface {
	Render(markdown string) (domain.Rendered, error)
}

// DocumentCache is an outbound (driven) port — the rendered-document cache
// the trail engine requires (ADR 0005 decision 9). Keys are the service's
// kind-prefixed addresses; values are display-ready Documents. Staleness is
// bounded by the focused-live policy, not by the cache: every render
// refreshes the pane the reader is looking at.
type DocumentCache interface {
	Get(key string) (domain.Document, bool)
	Put(key string, doc domain.Document)
}
