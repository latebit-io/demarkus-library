// Package port declares the hexagon's boundaries: the interfaces the core
// offers (inbound / driving) and the interfaces the core needs (outbound /
// driven). Adapters depend on these; the core never depends on adapters.
package port

import (
	"context"

	"github.com/latebit/demarkus-library/internal/core/domain"
)

// ReadingService is the inbound (driving) port — the use cases the reading room
// exposes to its primary adapters (the web adapter). Driving adapters depend on
// this interface, not on the concrete service.
//
// Every method takes the request context: it carries cancellation and — in
// broker mode — the logged-in reader's bearer token, threaded through to the
// outbound gateway (Phase 1b, ADR 0004).
type ReadingService interface {
	// Read fetches and renders the document at path into a display-ready form.
	Read(ctx context.Context, path string) (domain.Document, error)
	// Browse renders a directory listing (the stacks) at path.
	Browse(ctx context.Context, path string) (domain.Document, error)
	// History renders the edition history of the document at path.
	History(ctx context.Context, path string) (domain.Document, error)
	// Search renders the card catalog (LOOKUP) results for query under scope.
	Search(ctx context.Context, scope, query string) (domain.Document, error)
}

// WorldGateway is an outbound (driven) port — read from a demarkus world. The
// adapter translates transport status into domain errors and returns markdown
// bodies for the core to render. Phase 1a implements this over the direct QUIC
// fetch client (ignores the context's identity); the broker adapter reads the
// per-request bearer from ctx.
type WorldGateway interface {
	Fetch(ctx context.Context, path string) (domain.RawDocument, error)
	List(ctx context.Context, path string) (domain.RawDocument, error)
	Versions(ctx context.Context, path string) (domain.RawDocument, error)
	Lookup(ctx context.Context, scope, query string) (domain.RawDocument, error)
}

// Renderer is an outbound (driven) port — markdown to sanitized HTML.
type Renderer interface {
	Render(markdown string) (string, error)
}
