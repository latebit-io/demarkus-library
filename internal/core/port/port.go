// Package port declares the hexagon's boundaries: the interfaces the core
// offers (inbound / driving) and the interfaces the core needs (outbound /
// driven). Adapters depend on these; the core never depends on adapters.
package port

import "github.com/latebit/demarkus-library/internal/core/domain"

// ReadingService is the inbound (driving) port — the use cases the reading room
// exposes to its primary adapters (the web adapter). Driving adapters depend on
// this interface, not on the concrete service.
type ReadingService interface {
	// Read fetches and renders the document at path into a display-ready form.
	Read(path string) (domain.Document, error)
}

// WorldGateway is an outbound (driven) port — fetch a raw document from a
// demarkus world. The adapter translates transport status into domain errors.
type WorldGateway interface {
	Fetch(path string) (domain.RawDocument, error)
}

// Renderer is an outbound (driven) port — markdown to sanitized HTML.
type Renderer interface {
	Render(markdown string) (string, error)
}
