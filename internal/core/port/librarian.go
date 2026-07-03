package port

import (
	"context"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// Librarian is the inbound port for the Phase 4 AI librarian: ask a
// natural-language question about the universe, get back a stream of events —
// answer tokens, a visible tool-call trace, done (plan D4's vocabulary).
//
// The implementation (the nib-backed adapter) drives the reading ports as its
// tools, so every read it makes carries ctx — the asking reader's identity in
// broker mode — and it can see exactly what the reader can see (plan D1). The
// web adapter depends on this interface, never on the agent machinery.
type Librarian interface {
	// Ask streams the librarian's work on question within the conversation
	// keyed by conversation (server-side state; the key never carries
	// content — a session-scoped identifier). The channel closes after a
	// LibrarianDone or LibrarianError event. ctx bounds the run: cancelling
	// it (the client left) stops the agent and ends the stream.
	//
	// One ask at a time per conversation: a second Ask while one is in
	// flight returns domain.ErrLibrarianBusy.
	Ask(ctx context.Context, conversation, question string) (<-chan domain.LibrarianEvent, error)

	// History returns the conversation's completed exchanges in order — the
	// transcript the librarian pane renders. Memory-only (no world read, no
	// error); an unknown conversation is simply empty.
	History(conversation string) []domain.LibrarianExchange
}
