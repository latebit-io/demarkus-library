package domain

import (
	"errors"
	"fmt"
	"time"
)

// ErrLibrarianBusy is returned by the librarian port when a conversation
// already has a run in flight — one ask at a time per conversation (plan D7's
// single-flight). The caller retries after the current stream finishes.
var ErrLibrarianBusy = errors.New("librarian is already answering this conversation")

// LibrarianLimitError is returned when a conversation has used its asks for
// the rolling hour (plan D7's cost guard). RetryAfter is when the oldest
// counted ask ages out.
type LibrarianLimitError struct {
	RetryAfter time.Duration
}

func (e *LibrarianLimitError) Error() string {
	return fmt.Sprintf("librarian ask limit reached; try again in %s", e.RetryAfter.Round(time.Minute))
}

// LibrarianAsk is one question put to the librarian.
type LibrarianAsk struct {
	// Conversation keys the server-side conversation: a session-scoped
	// identifier that never carries content.
	Conversation string
	Question     string
	// Context is the reader's current view (plan D5: trail = context). It
	// rides this run only and never enters the transcript; "" means none.
	Context string
	Persona LibrarianPersona
}

// LibrarianPersona is how the room presents its librarian (ADR 0008): the name
// it answers to, the room's word for its universe, and house instructions
// from the hub world's branding. The zero value is the stock librarian.
type LibrarianPersona struct {
	Name         string
	Universe     string
	Instructions string
}

// MaxLibrarianInstructions bounds house instructions in characters (runes): a
// voice and a focus, not a second prompt.
const MaxLibrarianInstructions = 2048

// LibrarianEventKind names the stream vocabulary the librarian emits — the
// same three-event shape the SSE spike proved (plan D4): tokens accumulate
// into the answer, traces narrate the tool calls, done ends the stream.
type LibrarianEventKind string

const (
	// LibrarianToken is one streamed fragment of the answer's text.
	LibrarianToken LibrarianEventKind = "token"
	// LibrarianTrace is one step of the visible tool-call trace — watch the
	// librarian work the catalog.
	LibrarianTrace LibrarianEventKind = "trace"
	// LibrarianSourceOpened names a document the librarian opened: Text is
	// its title, Ref its address.
	LibrarianSourceOpened LibrarianEventKind = "source"
	// LibrarianAnswer carries one completed assistant message in full — the
	// authoritative text the token fragments assembled toward. Tokens are
	// best-effort (droppable under backpressure); consumers that render the
	// final answer reconcile on this event.
	LibrarianAnswer LibrarianEventKind = "answer"
	// LibrarianDone ends the stream; the answer is complete (or the run was
	// stopped by its turn cap or the reader — a trace line says so first).
	LibrarianDone LibrarianEventKind = "done"
	// LibrarianError reports a failed run; the stream ends after it.
	LibrarianError LibrarianEventKind = "error"
)

// LibrarianEvent is one frame of a librarian answer stream. Text carries the
// token fragment, trace line, source title, or error message as plain text —
// the consumer escapes or renders it for its own surface. Ref is the document
// a trace step touched or a source names; zero when there is none.
type LibrarianEvent struct {
	Kind LibrarianEventKind
	Text string
	Ref  Ref
}

// LibrarianStep is one line of an exchange's recorded trace; Ref is the
// document the step touched, zero when none.
type LibrarianStep struct {
	Text string
	Ref  Ref
}

// LibrarianSource is a document the librarian opened while answering.
type LibrarianSource struct {
	Ref
	Title string
}

// LibrarianExchange is one question/answer pair of a conversation, with the
// work behind it — what the librarian pane renders as transcript. Answer is
// markdown (the model's text); the presentation layer renders it like any
// document body. Stopped marks a run the reader ended; its Answer may be
// partial or empty.
type LibrarianExchange struct {
	Question string
	Answer   string
	Steps    []LibrarianStep
	Sources  []LibrarianSource
	Stopped  bool
}
