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
	// tag pages use tag=<tag>.
	Lookup(ctx context.Context, world, scope, query, filter string) (domain.RawDocument, error)
}

// Renderer is an outbound (driven) port — markdown to sanitized HTML plus
// the properties parsed from a leading frontmatter fence.
type Renderer interface {
	Render(markdown string) (domain.Rendered, error)
}
