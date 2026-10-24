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
	// Ask starts a run on ask.Question and subscribes to it, as Attach does.
	// The run keeps ctx's values (the reader's bearer), not its cancellation:
	// it outlives the subscription, within the implementation's bounds.
	//
	// One ask at a time per conversation: a second Ask while one is in
	// flight returns domain.ErrLibrarianBusy; an ask past the conversation's
	// hourly budget returns a *domain.LibrarianLimitError.
	Ask(ctx context.Context, ask domain.LibrarianAsk) (<-chan domain.LibrarianEvent, error)

	// Attach subscribes to the run of the ask with id askID: what it has done
	// so far, then live events; the channel closes after LibrarianDone or when
	// ctx ends. False when the conversation holds no such ask.
	Attach(ctx context.Context, conversation, askID string) (<-chan domain.LibrarianEvent, bool)

	// History returns the conversation's completed exchanges in order — the
	// transcript the librarian pane renders. Memory-only (no world read, no
	// error); an unknown conversation is simply empty.
	History(conversation string) []domain.LibrarianExchange

	// Stop ends the conversation's run in flight, keeping whatever was
	// answered so far as a stopped exchange. With no run in flight it does
	// nothing.
	Stop(conversation string)

	// Reset starts the conversation over: the transcript clears, the ask
	// budget does not. Returns domain.ErrLibrarianBusy while a run is in
	// flight.
	Reset(conversation string) error
}
