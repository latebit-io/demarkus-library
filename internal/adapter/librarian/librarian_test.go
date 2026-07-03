package librarian

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/nib/ai/llm"
)

// scriptedProvider serves pre-built streams, one per Stream call, and
// records the messages of every call so tests can assert transcript
// continuity. An exhausted queue keeps serving plain-text turns so a
// misbehaving loop terminates instead of wedging the test.
type scriptedProvider struct {
	mu    sync.Mutex
	turns [][]llm.StreamEvent
	calls [][]llm.Message
}

func (p *scriptedProvider) Stream(_ context.Context, msgs []llm.Message, _ []llm.ToolDef) (<-chan llm.StreamEvent, error) {
	p.mu.Lock()
	snapshot := make([]llm.Message, len(msgs))
	copy(snapshot, msgs)
	p.calls = append(p.calls, snapshot)
	var events []llm.StreamEvent
	if len(p.turns) > 0 {
		events = p.turns[0]
		p.turns = p.turns[1:]
	} else {
		events = []llm.StreamEvent{{Token: "fallback"}, {Done: true}}
	}
	p.mu.Unlock()

	ch := make(chan llm.StreamEvent, len(events))
	for _, ev := range events {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

func (p *scriptedProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func textTurn(tokens ...string) []llm.StreamEvent {
	out := make([]llm.StreamEvent, 0, len(tokens)+1)
	for _, tok := range tokens {
		out = append(out, llm.StreamEvent{Token: tok})
	}
	return append(out, llm.StreamEvent{Done: true})
}

func toolTurn(id, name, args string) []llm.StreamEvent {
	return []llm.StreamEvent{{Done: true, ToolCalls: []llm.ToolCall{{
		ID: id, Type: "function",
		Function: llm.FunctionCall{Name: name, Arguments: args},
	}}}}
}

// collect drains an Ask stream to completion with a deadline.
func collect(t *testing.T, ch <-chan domain.LibrarianEvent) []domain.LibrarianEvent {
	t.Helper()
	var out []domain.LibrarianEvent
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-deadline:
			t.Fatalf("stream did not close; got %d events: %+v", len(out), out)
		}
	}
}

func kinds(evs []domain.LibrarianEvent) []domain.LibrarianEventKind {
	out := make([]domain.LibrarianEventKind, len(evs))
	for i, ev := range evs {
		out[i] = ev.Kind
	}
	return out
}

func newTestLibrarian(t *testing.T, p llm.Provider, ports *fakePorts) *Librarian {
	t.Helper()
	l, err := New(Config{
		Provider:     p,
		Reader:       ports,
		Graph:        ports,
		Map:          ports,
		DefaultWorld: "root",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return l
}

func TestAsk_StreamsTraceTokensAnswerDone(t *testing.T) {
	t.Parallel()

	provider := &scriptedProvider{turns: [][]llm.StreamEvent{
		toolTurn("c1", "open", `{"path":"/ops/deploy.md"}`),
		textTurn("The runbook ", "is at hand."),
	}}
	ports := newFakePorts()
	l := newTestLibrarian(t, provider, ports)

	ch, err := l.Ask(context.Background(), "conv-1", "where is the deploy runbook?")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	evs := collect(t, ch)

	got := kinds(evs)
	want := []domain.LibrarianEventKind{
		domain.LibrarianTrace,  // open path="/ops/deploy.md"
		domain.LibrarianToken,  // "The runbook "
		domain.LibrarianToken,  // "is at hand."
		domain.LibrarianAnswer, // reconciled full message
		domain.LibrarianDone,
	}
	if len(got) != len(want) {
		t.Fatalf("event kinds = %v; want %v (events: %+v)", got, want, evs)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event kinds = %v; want %v", got, want)
		}
	}
	if !strings.Contains(evs[0].Text, `open path="/ops/deploy.md"`) {
		t.Errorf("trace = %q; want the tool call rendered", evs[0].Text)
	}
	if evs[3].Text != "The runbook is at hand." {
		t.Errorf("answer = %q; want the assembled message", evs[3].Text)
	}
	if got := ports.rawCalls(); len(got) != 1 || got[0] != "root:/ops/deploy.md" {
		t.Errorf("Raw calls = %v; want one read of root:/ops/deploy.md (default world applied)", got)
	}
}

func TestAsk_SecondAskCarriesHistory(t *testing.T) {
	t.Parallel()

	provider := &scriptedProvider{turns: [][]llm.StreamEvent{
		textTurn("First answer."),
		textTurn("Second answer."),
	}}
	l := newTestLibrarian(t, provider, newFakePorts())

	collect(t, mustAsk(t, l, "conv-1", "first question"))
	collect(t, mustAsk(t, l, "conv-1", "second question"))

	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.calls) != 2 {
		t.Fatalf("provider calls = %d; want 2", len(provider.calls))
	}
	second := provider.calls[1]
	// system + user1 + assistant1 + user2
	if len(second) != 4 {
		t.Fatalf("second transcript length = %d; want 4: %+v", len(second), second)
	}
	if second[0].Role != "system" {
		t.Errorf("transcript[0].Role = %q; want system", second[0].Role)
	}
	if second[1].Content != "first question" || second[2].Content != "First answer." {
		t.Errorf("history not carried: %+v", second)
	}
	if second[3].Content != "second question" {
		t.Errorf("transcript[3] = %+v; want the new question", second[3])
	}
}

func TestAsk_BusyWhileRunInFlight(t *testing.T) {
	t.Parallel()

	// A provider that blocks until released keeps the first run in flight.
	release := make(chan struct{})
	provider := &blockingProvider{release: release}
	l := newTestLibrarian(t, provider, newFakePorts())

	ch := mustAsk(t, l, "conv-1", "slow question")
	if _, err := l.Ask(context.Background(), "conv-1", "impatient question"); !errors.Is(err, domain.ErrLibrarianBusy) {
		t.Errorf("second Ask error = %v; want ErrLibrarianBusy", err)
	}
	// A different conversation is not blocked by conv-1's run.
	other := mustAsk(t, l, "conv-2", "parallel question")

	close(release)
	collect(t, ch)
	collect(t, other)

	// After the runs finish, the conversation accepts asks again.
	collect(t, mustAsk(t, l, "conv-1", "follow-up"))
}

func TestAsk_TurnCapTracedAndDone(t *testing.T) {
	t.Parallel()

	provider := &scriptedProvider{turns: [][]llm.StreamEvent{
		toolTurn("c1", "worlds", "{}"),
		toolTurn("c2", "worlds", "{}"),
	}}
	ports := newFakePorts()
	l, err := New(Config{
		Provider: provider, Reader: ports, Graph: ports, Map: ports,
		DefaultWorld: "root", MaxTurns: 2,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	evs := collect(t, mustAsk(t, l, "conv-1", "loop forever"))
	var capTrace, done bool
	for _, ev := range evs {
		if ev.Kind == domain.LibrarianTrace && strings.Contains(ev.Text, "turn cap") {
			capTrace = true
		}
		if ev.Kind == domain.LibrarianDone {
			done = true
		}
		if ev.Kind == domain.LibrarianError {
			t.Errorf("turn cap surfaced as error: %+v", ev)
		}
	}
	if !capTrace || !done {
		t.Errorf("capTrace=%v done=%v; want both (events: %+v)", capTrace, done, evs)
	}
	if provider.callCount() != 2 {
		t.Errorf("provider calls = %d; want 2 (cap enforced)", provider.callCount())
	}
}

func TestAsk_EmptyQuestionRejected(t *testing.T) {
	t.Parallel()

	l := newTestLibrarian(t, &scriptedProvider{}, newFakePorts())
	if _, err := l.Ask(context.Background(), "conv-1", "   "); err == nil {
		t.Error("Ask with blank question succeeded; want error")
	}
}

func TestAsk_ClientCancelEndsStream(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	defer close(release)
	provider := &blockingProvider{release: release}
	l := newTestLibrarian(t, provider, newFakePorts())

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := l.Ask(ctx, "conv-1", "question the reader abandons")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	cancel()

	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // stream closed promptly after cancel — pass
			}
		case <-deadline:
			t.Fatal("stream did not close after ctx cancel")
		}
	}
}

func mustAsk(t *testing.T, l *Librarian, conv, q string) <-chan domain.LibrarianEvent {
	t.Helper()
	ch, err := l.Ask(context.Background(), conv, q)
	if err != nil {
		t.Fatalf("Ask(%s): %v", conv, err)
	}
	return ch
}

// blockingProvider parks Stream until released, then answers with one text
// turn. Exercises the busy path and client-cancel unwinding.
type blockingProvider struct{ release <-chan struct{} }

func (p *blockingProvider) Stream(ctx context.Context, _ []llm.Message, _ []llm.ToolDef) (<-chan llm.StreamEvent, error) {
	ch := make(chan llm.StreamEvent, 2)
	go func() {
		defer close(ch)
		select {
		case <-p.release:
			ch <- llm.StreamEvent{Token: "released"}
			ch <- llm.StreamEvent{Done: true}
		case <-ctx.Done():
		}
	}()
	return ch, nil
}

func TestHistory_ReturnsCompletedExchanges(t *testing.T) {
	t.Parallel()

	provider := &scriptedProvider{turns: [][]llm.StreamEvent{
		toolTurn("c1", "worlds", "{}"),
		textTurn("The floor is the universe view."),
	}}
	l := newTestLibrarian(t, provider, newFakePorts())

	if got := l.History("conv-1"); len(got) != 0 {
		t.Errorf("fresh conversation History = %v; want empty", got)
	}
	collect(t, mustAsk(t, l, "conv-1", "what is the floor?"))

	got := l.History("conv-1")
	if len(got) != 1 {
		t.Fatalf("History length = %d; want 1: %+v", len(got), got)
	}
	if got[0].Question != "what is the floor?" || got[0].Answer != "The floor is the universe view." {
		t.Errorf("exchange = %+v; want the completed Q/A", got[0])
	}
}
