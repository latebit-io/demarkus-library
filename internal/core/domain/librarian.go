package domain

import "errors"

// ErrLibrarianBusy is returned by the librarian port when a conversation
// already has a run in flight — one ask at a time per conversation (plan D7's
// single-flight). The caller retries after the current stream finishes.
var ErrLibrarianBusy = errors.New("librarian is already answering this conversation")

// LibrarianEventKind names the stream vocabulary the librarian emits — the
// same three-event shape the SSE spike proved (plan D4): tokens accumulate
// into the answer, traces narrate the tool calls, done ends the stream.
type LibrarianEventKind string

const (
	// LibrarianToken is one streamed fragment of the answer's text.
	LibrarianToken LibrarianEventKind = "token"
	// LibrarianTrace is one line of the visible tool-call trace — watch the
	// librarian work the catalog.
	LibrarianTrace LibrarianEventKind = "trace"
	// LibrarianAnswer carries one completed assistant message in full — the
	// authoritative text the token fragments assembled toward. Tokens are
	// best-effort (droppable under backpressure); consumers that render the
	// final answer reconcile on this event.
	LibrarianAnswer LibrarianEventKind = "answer"
	// LibrarianDone ends the stream; the answer is complete (or the run was
	// stopped by its turn cap — a trace line says so first).
	LibrarianDone LibrarianEventKind = "done"
	// LibrarianError reports a failed run; the stream ends after it.
	LibrarianError LibrarianEventKind = "error"
)

// LibrarianEvent is one frame of a librarian answer stream. Text carries the
// token fragment, trace line, or error message; it is plain text — the
// consumer escapes or renders it for its own surface.
type LibrarianEvent struct {
	Kind LibrarianEventKind
	Text string
}

// LibrarianExchange is one completed question/answer pair of a conversation —
// what the librarian pane renders as transcript. Answer is markdown (the
// model's text); the presentation layer renders it like any document body.
type LibrarianExchange struct {
	Question string
	Answer   string
}
