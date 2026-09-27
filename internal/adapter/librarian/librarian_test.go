package librarian

import (
	"context"
	"errors"
	"log/slog"
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
		Catalog:      ports,
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

	ch, err := l.Ask(context.Background(), domain.LibrarianAsk{Conversation: "conv-1", Question: "where is the deploy runbook?"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	evs := collect(t, ch)

	got := kinds(evs)
	want := []domain.LibrarianEventKind{
		domain.LibrarianTrace,        // opened /ops/deploy.md
		domain.LibrarianSourceOpened, // the open succeeded
		domain.LibrarianToken,        // "The runbook "
		domain.LibrarianToken,        // "is at hand."
		domain.LibrarianAnswer,       // reconciled full message
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
	deploy := domain.Ref{World: "root", Path: "/ops/deploy.md"}
	if evs[0].Text != "opened" || evs[0].Ref != deploy {
		t.Errorf("trace = %+v; want the open narrated with its document", evs[0])
	}
	if evs[1].Text != "Deploy runbook" || evs[1].Ref != deploy {
		t.Errorf("source = %+v; want the opened document with its title", evs[1])
	}
	if evs[4].Text != "The runbook is at hand." {
		t.Errorf("answer = %q; want the assembled message", evs[4].Text)
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
	if _, err := l.Ask(context.Background(), domain.LibrarianAsk{Conversation: "conv-1", Question: "impatient question"}); !errors.Is(err, domain.ErrLibrarianBusy) {
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
		textTurn("From what I found: ", "the floor."),
	}}
	ports := newFakePorts()
	l, err := New(Config{
		Provider: provider, Reader: ports, Catalog: ports, Graph: ports, Map: ports,
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
	// Two capped turns, then one tool-less turn for the answer the cap promised.
	if provider.callCount() != 3 {
		t.Errorf("provider calls = %d; want 3 (cap enforced, then the final word)", provider.callCount())
	}
	provider.mu.Lock()
	final := provider.calls[2]
	provider.mu.Unlock()
	if last := final[len(final)-1]; last.Role != "user" || last.Content != capNudge {
		t.Errorf("final turn ends with %+v; want the cap nudge", last)
	}
	if hist := l.History("conv-1"); len(hist) != 1 || hist[0].Answer != "From what I found: the floor." {
		t.Errorf("History = %+v; want the capped run's final answer kept", hist)
	}
}

func TestAsk_EmptyQuestionRejected(t *testing.T) {
	t.Parallel()

	l := newTestLibrarian(t, &scriptedProvider{}, newFakePorts())
	if _, err := l.Ask(context.Background(), domain.LibrarianAsk{Conversation: "conv-1", Question: "   "}); err == nil {
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
	ch, err := l.Ask(ctx, domain.LibrarianAsk{Conversation: "conv-1", Question: "question the reader abandons"})
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
	ch, err := l.Ask(context.Background(), domain.LibrarianAsk{Conversation: conv, Question: q})
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

func TestAsk_TrailContextReachesModelNotHistory(t *testing.T) {
	t.Parallel()

	provider := &scriptedProvider{turns: [][]llm.StreamEvent{
		textTurn("Answer one."),
		textTurn("Answer two."),
	}}
	l := newTestLibrarian(t, provider, newFakePorts())

	ch, err := l.Ask(context.Background(), domain.LibrarianAsk{Conversation: "conv-1", Question: "what is this?", Context: "<reader-context>focused: /x.md</reader-context>"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	collect(t, ch)

	provider.mu.Lock()
	first := provider.calls[0]
	provider.mu.Unlock()
	last := first[len(first)-1]
	if last.Role != "user" || !strings.HasPrefix(last.Content, "<reader-context>") ||
		!strings.Contains(last.Content, "what is this?") {
		t.Errorf("model did not receive context-prefixed question: %+v", last)
	}

	// The saved transcript stays clean — History shows the bare question.
	hist := l.History("conv-1")
	if len(hist) != 1 || hist[0].Question != "what is this?" {
		t.Errorf("History polluted by context: %+v", hist)
	}

	// A follow-up WITHOUT context must not inherit the stale injection.
	collect(t, mustAsk(t, l, "conv-1", "and now?"))
	provider.mu.Lock()
	second := provider.calls[1]
	provider.mu.Unlock()
	for _, m := range second {
		// The system prompt legitimately names the tag; only user turns
		// would carry a stale injection.
		if m.Role == "user" && strings.Contains(m.Content, "<reader-context>") {
			t.Errorf("stale context leaked into the next run: %+v", m)
		}
	}
}

func TestAsk_HistoryReplaysAnswersNotToolWork(t *testing.T) {
	t.Parallel()

	provider := &scriptedProvider{turns: [][]llm.StreamEvent{
		toolTurn("c1", "open", `{"path":"/ops/deploy.md"}`),
		textTurn("It is in ops."),
		textTurn("Second answer."),
	}}
	l := newTestLibrarian(t, provider, newFakePorts())
	collect(t, mustAsk(t, l, "conv-1", "where is the runbook?"))
	collect(t, mustAsk(t, l, "conv-1", "and rollback?"))

	provider.mu.Lock()
	second := provider.calls[len(provider.calls)-1]
	provider.mu.Unlock()
	if len(second) != 4 {
		t.Fatalf("second transcript = %d messages; want system + Q/A + question: %+v", len(second), second)
	}
	for _, m := range second {
		if m.Role == "tool" || len(m.ToolCalls) > 0 {
			t.Errorf("tool work replayed into the next ask: %+v", m)
		}
	}
	if !strings.HasPrefix(second[2].Content, "It is in ops.") ||
		!strings.Contains(second[2].Content, "Opened for this answer: mark://root/ops/deploy.md") {
		t.Errorf("past answer = %q; want the answer and a note of its sources", second[2].Content)
	}
}

func TestAsk_HistoryWindowIsBounded(t *testing.T) {
	t.Parallel()

	provider := &scriptedProvider{turns: [][]llm.StreamEvent{
		textTurn("One."), textTurn("Two."), textTurn("Three."),
	}}
	ports := newFakePorts()
	l, err := New(Config{Provider: provider, Reader: ports, Catalog: ports, Graph: ports, Map: ports, HistoryExchanges: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, q := range []string{"q1", "q2", "q3"} {
		collect(t, mustAsk(t, l, "conv-1", q))
	}
	provider.mu.Lock()
	third := provider.calls[2]
	provider.mu.Unlock()
	if len(third) != 4 || third[1].Content != "q2" || third[3].Content != "q3" {
		t.Errorf("third transcript = %+v; want system, q2, Two., q3", third)
	}
	if got := len(l.History("conv-1")); got != 3 {
		t.Errorf("History = %d exchanges; the pane keeps all of them, want 3", got)
	}
}

func TestStop_EndsRunAndKeepsStoppedExchange(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	defer close(release)
	l := newTestLibrarian(t, &blockingProvider{release: release}, newFakePorts())

	ch := mustAsk(t, l, "conv-1", "a long question")
	l.Stop("conv-1")
	evs := collect(t, ch)

	var stoppedTrace bool
	for _, ev := range evs {
		stoppedTrace = stoppedTrace || (ev.Kind == domain.LibrarianTrace && ev.Text == "stopped at your request")
	}
	if !stoppedTrace || evs[len(evs)-1].Kind != domain.LibrarianDone {
		t.Errorf("events = %+v; want the stop narrated, then done", evs)
	}
	hist := l.History("conv-1")
	if len(hist) != 1 || !hist[0].Stopped || hist[0].Question != "a long question" {
		t.Errorf("History = %+v; want one stopped exchange", hist)
	}
	l.Stop("conv-1") // idle: a no-op, not a panic
	l.Stop("nobody")
}

func TestReset_ClearsTranscriptNotWhileRunning(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	l := newTestLibrarian(t, &blockingProvider{release: release}, newFakePorts())

	ch := mustAsk(t, l, "conv-1", "question")
	if err := l.Reset("conv-1"); !errors.Is(err, domain.ErrLibrarianBusy) {
		t.Errorf("Reset during a run = %v; want ErrLibrarianBusy", err)
	}
	close(release)
	collect(t, ch)
	if err := l.Reset("conv-1"); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if hist := l.History("conv-1"); len(hist) != 0 {
		t.Errorf("History after Reset = %+v; want empty", hist)
	}
	if err := l.Reset("nobody"); err != nil {
		t.Errorf("Reset of an unknown conversation = %v; want nil", err)
	}
}

func TestAsk_HourlyBudget(t *testing.T) {
	t.Parallel()

	provider := &scriptedProvider{}
	ports := newFakePorts()
	l, err := New(Config{Provider: provider, Reader: ports, Catalog: ports, Graph: ports, Map: ports, AsksPerHour: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	clock := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return clock }

	collect(t, mustAsk(t, l, "conv-1", "one"))
	clock = clock.Add(10 * time.Minute)
	collect(t, mustAsk(t, l, "conv-1", "two"))
	if err := l.Reset("conv-1"); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	_, err = l.Ask(context.Background(), domain.LibrarianAsk{Conversation: "conv-1", Question: "three"})
	var limit *domain.LibrarianLimitError
	if !errors.As(err, &limit) || limit.RetryAfter != 50*time.Minute {
		t.Fatalf("third ask err = %v; want a limit error retrying in 50m (Reset must not refill the budget)", err)
	}
	collect(t, mustAsk(t, l, "conv-2", "another reader")) // budgets are per conversation

	clock = clock.Add(50 * time.Minute)
	collect(t, mustAsk(t, l, "conv-1", "three"))
}

func TestAsk_PersonaShapesSystemPrompt(t *testing.T) {
	t.Parallel()

	provider := &scriptedProvider{}
	l := newTestLibrarian(t, provider, newFakePorts())
	ch, err := l.Ask(context.Background(), domain.LibrarianAsk{
		Conversation: "conv-1", Question: "hello",
		Persona: domain.LibrarianPersona{Name: "Ada", Universe: "Library", Instructions: "Speak plainly."},
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	collect(t, ch)
	collect(t, mustAsk(t, l, "conv-2", "stock"))

	provider.mu.Lock()
	branded, stock := provider.calls[0][0].Content, provider.calls[1][0].Content
	provider.mu.Unlock()
	if !strings.HasPrefix(branded, "You are Ada, the librarian of a demarkus library") ||
		!strings.HasSuffix(branded, "Speak plainly.") {
		t.Errorf("branded system prompt = %q", branded)
	}
	if !strings.HasPrefix(stock, "You are the librarian of a demarkus universe") || strings.Contains(stock, "House instructions") {
		t.Errorf("stock system prompt = %q", stock)
	}
}

func TestSystemPrompt_CapsInstructionsInCharacters(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("é", domain.MaxLibrarianInstructions+10)
	prompt := systemPrompt(domain.LibrarianPersona{Instructions: long})
	if got := strings.Count(prompt, "é"); got != domain.MaxLibrarianInstructions {
		t.Errorf("prompt keeps %d characters of the instructions; want %d", got, domain.MaxLibrarianInstructions)
	}
}

func TestAsk_LogsUsageWithoutTheSessionKey(t *testing.T) {
	t.Parallel()

	var logs strings.Builder
	var mu sync.Mutex
	ports := newFakePorts()
	l, err := New(Config{
		Provider: &scriptedProvider{}, Reader: ports, Catalog: ports, Graph: ports, Map: ports,
		Logger: slog.New(slog.NewTextHandler(&lockedWriter{mu: &mu, w: &logs}, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	collect(t, mustAsk(t, l, "secret-session-cookie", "hello"))

	mu.Lock()
	line := logs.String()
	mu.Unlock()
	if !strings.Contains(line, `msg="librarian ask"`) || !strings.Contains(line, "outcome=answered") {
		t.Errorf("usage line = %q", line)
	}
	if strings.Contains(line, "secret-session-cookie") {
		t.Errorf("session key logged: %q", line)
	}
}

// lockedWriter serializes log writes the test reads concurrently.
type lockedWriter struct {
	mu *sync.Mutex
	w  *strings.Builder
}

func (lw *lockedWriter) Write(p []byte) (int, error) {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	return lw.w.Write(p)
}
