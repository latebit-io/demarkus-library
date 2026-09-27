package web

import (
	"context"
	"strings"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// librarianIdentity resolves how the room presents its librarian: the hub
// world's declaration (ADR 0008) over the manifest's term, over the stock
// name. The nav, the pane title, and the system prompt all read it here.
type librarianIdentity struct {
	brands *WorldBrands // nil ⇒ no in-world declaration
	terms  Terms
}

// name is the librarian's display name.
func (id librarianIdentity) name(ctx context.Context) string {
	if hub := id.brands.RoomLibrarian(ctx); hub.Name != "" {
		return hub.Name
	}
	if id.terms.Librarian != "" {
		return id.terms.Librarian
	}
	return DefaultTerms().Librarian
}

// persona is the identity the librarian itself answers as. The stock word is
// a role, not a name, so it leaves the persona unnamed.
func (id librarianIdentity) persona(ctx context.Context) domain.LibrarianPersona {
	name := id.name(ctx)
	if strings.EqualFold(name, DefaultTerms().Librarian) {
		name = ""
	}
	return domain.LibrarianPersona{
		Name:         name,
		Universe:     id.terms.Universe,
		Instructions: id.brands.RoomLibrarian(ctx).Instructions,
	}
}
