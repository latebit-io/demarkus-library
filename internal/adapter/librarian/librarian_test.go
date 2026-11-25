package librarian

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/nib/ai/llm"
	"github.com/latebit-io/nib/ai/llmtest"
)

// answers scripts n plain answers, for tests whose runs only need to end
// answered: past its script a Scripted hangs up, which ends a run in error.
func answers(n int) *llmtest.Scripted {
	return llmtest.NewScripted(slices.Repeat([][]llm.StreamEvent{llmtest.Text("ok")}, n)...)
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

func TestAsk_StreamsTraceAnswerDone(t *testing.T) {
	t.Parallel()

	provider := llmtest.NewScripted(
		llmtest.ToolCalls(llmtest.Call("c1", "open", `{"path":"/ops/deploy.md"}`)),
		llmtest.Text("The runbook ", "is at hand."),
	)
	ports := newFakePorts()
	l := newTestLibrarian(t, provider, ports)

	ch, err := l.Ask(context.Background(), domain.LibrarianAsk{Conversation: "conv-1", Question: "where is the deploy runbook?"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	// Tokens are best-effort: a subscription that falls a whole message
	// behind gets the reconciling answer alone.
	evs := slices.DeleteFunc(collect(t, ch), func(ev domain.LibrarianEvent) bool { return ev.Kind == domain.LibrarianToken })

	got := kinds(evs)
	want := []domain.LibrarianEventKind{
		domain.LibrarianTrace,        // opened /ops/deploy.md
		domain.LibrarianSourceOpened, // the open succeeded
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
	if evs[2].Text != "The runbook is at hand." {
		t.Errorf("answer = %q; want the assembled message", evs[2].Text)
	}
	if got := ports.rawCalls(); len(got) != 1 || got[0] != "root:/ops/deploy.md" {
		t.Errorf("Raw calls = %v; want one read of root:/ops/deploy.md (default world applied)", got)
	}
}

func TestAsk_SecondAskCarriesHistory(t *testing.T) {
	t.Parallel()

	provider := llmtest.NewScripted(
		llmtest.Text("First answer."),
		llmtest.Text("Second answer."),
	)
	l := newTestLibrarian(t, provider, newFakePorts())

	collect(t, mustAsk(t, l, "conv-1", "first question"))
	collect(t, mustAsk(t, l, "conv-1", "second question"))

	if provider.Calls() != 2 {
		t.Fatalf("provider calls = %d; want 2", provider.Calls())
	}
	second := provider.Requests()[1].Messages
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

	provider := llmtest.NewScripted(
		llmtest.ToolCalls(llmtest.Call("c1", "worlds", "{}")),
		llmtest.ToolCalls(llmtest.Call("c2", "worlds", "{}")),
		llmtest.Text("From what I found: ", "the floor."),
	)
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
	if provider.Calls() != 3 {
		t.Fatalf("provider calls = %d; want 3 (cap enforced, then the closing answer)", provider.Calls())
	}
	closing := provider.Requests()[2]
	if len(closing.Tools) != 0 {
		t.Errorf("closing turn offered %d tools; want none", len(closing.Tools))
	}
	if last := closing.Messages[len(closing.Messages)-1]; last.Role != "user" || last.Content != capNudge {
		t.Errorf("closing turn ends with %+v; want the cap nudge", last)
	}
	if hist := l.History("conv-1"); len(hist) != 1 || hist[0].Answer != "From what I found: the floor." {
		t.Errorf("History = %+v; want the capped run's final answer kept", hist)
	}
}

func TestAsk_EmptyQuestionRejected(t *testing.T) {
	t.Parallel()

	l := newTestLibrarian(t, llmtest.NewScripted(), newFakePorts())
	if _, err := l.Ask(context.Background(), domain.LibrarianAsk{Conversation: "conv-1", Question: "   "}); err == nil {
		t.Error("Ask with blank question succeeded; want error")
	}
}

func TestAsk_ClientCancelEndsStream(t *testing.T) {
	t.Parallel()

	l := newTestLibrarian(t, llmtest.Blocking(), newFakePorts())

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := l.Ask(ctx, domain.LibrarianAsk{Conversation: "conv-1", Question: "question the reader abandons"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	cancel()

	collect(t, ch) // fails unless the stream closes after the cancel
}

func mustAsk(t *testing.T, l *Librarian, conv, q string) <-chan domain.LibrarianEvent {
	t.Helper()
	ch, err := l.Ask(context.Background(), domain.LibrarianAsk{Conversation: conv, Question: q})
	if err != nil {
		t.Fatalf("Ask(%s): %v", conv, err)
	}
	return ch
}

// blockingProvider streams lead (if any), parks until released, then
// finishes with one text token: a run held in flight, mid-message with lead.
type blockingProvider struct {
	release <-chan struct{}
	lead    string
}

func (p *blockingProvider) Stream(ctx context.Context, _ []llm.Message, _ []llm.ToolDef) (<-chan llm.StreamEvent, error) {
	ch := make(chan llm.StreamEvent, 3)
	go func() {
		defer close(ch)
		if p.lead != "" {
			ch <- llm.StreamEvent{Token: p.lead}
		}
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

	provider := llmtest.NewScripted(
		llmtest.ToolCalls(llmtest.Call("c1", "worlds", "{}")),
		llmtest.Text("The floor is the universe view."),
	)
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

	provider := llmtest.NewScripted(
		llmtest.Text("Answer one."),
		llmtest.Text("Answer two."),
	)
	l := newTestLibrarian(t, provider, newFakePorts())

	ch, err := l.Ask(context.Background(), domain.LibrarianAsk{Conversation: "conv-1", Question: "what is this?", Context: "<reader-context>focused: /x.md</reader-context>"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	collect(t, ch)

	first := provider.Requests()[0].Messages
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
	second := provider.Requests()[1].Messages
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

	provider := llmtest.NewScripted(
		llmtest.ToolCalls(llmtest.Call("c1", "open", `{"path":"/ops/deploy.md"}`)),
		llmtest.Text("It is in ops."),
		llmtest.Text("Second answer."),
	)
	l := newTestLibrarian(t, provider, newFakePorts())
	collect(t, mustAsk(t, l, "conv-1", "where is the runbook?"))
	collect(t, mustAsk(t, l, "conv-1", "and rollback?"))

	second := provider.Requests()[2].Messages
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

	provider := llmtest.NewScripted(
		llmtest.Text("One."), llmtest.Text("Two."), llmtest.Text("Three."),
	)
	ports := newFakePorts()
	l, err := New(Config{Provider: provider, Reader: ports, Catalog: ports, Graph: ports, Map: ports, HistoryExchanges: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, q := range []string{"q1", "q2", "q3"} {
		collect(t, mustAsk(t, l, "conv-1", q))
	}
	third := provider.Requests()[2].Messages
	if len(third) != 4 || third[1].Content != "q2" || third[3].Content != "q3" {
		t.Errorf("third transcript = %+v; want system, q2, Two., q3", third)
	}
	if got := len(l.History("conv-1")); got != 3 {
		t.Errorf("History = %d exchanges; the pane keeps all of them, want 3", got)
	}
}

func TestStop_EndsRunAndKeepsStoppedExchange(t *testing.T) {
	t.Parallel()

	l := newTestLibrarian(t, llmtest.Blocking("half "), newFakePorts())

	ch := mustAsk(t, l, "conv-1", "a long question")
	if ev := <-ch; ev.Kind != domain.LibrarianToken {
		t.Fatalf("first event = %+v; want the streamed token", ev)
	}
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
	if len(hist) != 1 || !hist[0].Stopped || hist[0].Question != "a long question" || hist[0].Answer != "half " {
		t.Errorf("History = %+v; want one stopped exchange keeping what had streamed", hist)
	}
	l.Stop("conv-1") // idle: a no-op, not a panic
	l.Stop("nobody")
}

func TestAsk_ToolCallsOfOneTurnRunTogether(t *testing.T) {
	t.Parallel()

	provider := llmtest.NewScripted(
		llmtest.ToolCalls(
			llmtest.Call("c1", "open", `{"path":"/ops/deploy.md"}`),
			llmtest.Call("c2", "open", `{"path":"/ops/rollback.md"}`),
			llmtest.Call("c3", "worlds", "{}"),
		),
		llmtest.Text("Both read."),
	)
	ports := newFakePorts()
	l := newTestLibrarian(t, provider, ports)

	collect(t, mustAsk(t, l, "conv-1", "compare the two"))

	hist := l.History("conv-1")
	if len(hist) != 1 || len(hist[0].Steps) != 3 || hist[0].Answer != "Both read." {
		t.Fatalf("History = %+v; want three traced calls and the answer", hist)
	}
	// Sources land in the order the reads finish, so compare as a set.
	opened := map[string]bool{}
	for _, src := range hist[0].Sources {
		opened[src.Path] = true
	}
	if len(opened) != 2 || !opened["/ops/deploy.md"] || !opened["/ops/rollback.md"] {
		t.Errorf("sources = %+v; want both opened documents", hist[0].Sources)
	}
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

	ports := newFakePorts()
	l, err := New(Config{Provider: answers(4), Reader: ports, Catalog: ports, Graph: ports, Map: ports, AsksPerHour: 2})
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

	provider := answers(2)
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

	requests := provider.Requests()
	branded, stock := requests[0].Messages[0].Content, requests[1].Messages[0].Content
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
		Provider: answers(1), Reader: ports, Catalog: ports, Graph: ports, Map: ports,
		Logger: slog.New(slog.NewTextHandler(&lockedWriter{mu: &mu, w: &logs}, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	collect(t, mustAsk(t, l, "secret-session-cookie", "hello"))

	mu.Lock()
	line := logs.String()
	mu.Unlock()
	if !strings.Contains(line, `msg="librarian ask"`) || !strings.Contains(line, "outcome=answered") || !strings.Contains(line, "turns=1") {
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

// eventually polls cond until it holds or the deadline passes.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAsk_RunOutlivesSubscription(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	l := newTestLibrarian(t, &blockingProvider{release: release}, newFakePorts())
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := l.Ask(ctx, domain.LibrarianAsk{ID: "ask-1", Conversation: "conv-1", Question: "slow"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	cancel()
	collect(t, ch) // the subscription ends with its context

	again, ok := l.Attach(context.Background(), "conv-1", "ask-1")
	if !ok {
		t.Fatal("Attach found no run after the subscription dropped")
	}
	close(release)
	evs := collect(t, again)
	if evs[len(evs)-1].Kind != domain.LibrarianDone {
		t.Errorf("events = %+v; want done last", evs)
	}
	if hist := l.History("conv-1"); len(hist) != 1 || hist[0].Answer != "released" {
		t.Errorf("History = %+v; want the run answered, not abandoned", hist)
	}
}

func TestAsk_AbandonedAfterGrace(t *testing.T) {
	t.Parallel()

	ports := newFakePorts()
	l, err := New(Config{
		Provider: llmtest.Blocking(), Reader: ports, Catalog: ports, Graph: ports, Map: ports,
		ReconnectGrace: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := l.Ask(ctx, domain.LibrarianAsk{ID: "ask-1", Conversation: "conv-1", Question: "left behind"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	cancel()
	collect(t, ch)

	// Nobody reattaches: the grace ends the run and frees the conversation.
	eventually(t, "the abandoned exchange", func() bool { return len(l.History("conv-1")) == 1 })
	if hist := l.History("conv-1"); hist[0].Answer != "" {
		t.Errorf("abandoned exchange = %+v; want no answer", hist[0])
	}
	if _, err := l.Ask(t.Context(), domain.LibrarianAsk{Conversation: "conv-1", Question: "back again"}); err != nil {
		t.Errorf("Ask after abandonment = %v; want the slot free", err)
	}
}

func TestAsk_RunTimeoutIsNarrated(t *testing.T) {
	t.Parallel()

	ports := newFakePorts()
	l, err := New(Config{
		Provider: llmtest.Blocking(), Reader: ports, Catalog: ports, Graph: ports, Map: ports,
		RunTimeout: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	evs := collect(t, mustAsk(t, l, "conv-1", "too slow"))
	var narrated bool
	for _, ev := range evs {
		narrated = narrated || (ev.Kind == domain.LibrarianTrace && strings.Contains(ev.Text, "time limit"))
	}
	if !narrated || evs[len(evs)-1].Kind != domain.LibrarianDone {
		t.Errorf("events = %+v; want the timeout narrated, then done", evs)
	}
}

func TestAttach_ReplaysThenFollows(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	l := newTestLibrarian(t, &blockingProvider{release: release, lead: "half "}, newFakePorts())
	first, err := l.Ask(context.Background(), domain.LibrarianAsk{ID: "ask-1", Conversation: "conv-1", Question: "q"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if ev := <-first; ev.Kind != domain.LibrarianToken || ev.Text != "half " {
		t.Fatalf("first event = %+v; want the streamed token", ev)
	}

	second, ok := l.Attach(context.Background(), "conv-1", "ask-1")
	if !ok {
		t.Fatal("Attach found no run in flight")
	}
	// The message so far replays as one token before anything live.
	if ev := <-second; ev.Kind != domain.LibrarianToken || ev.Text != "half " {
		t.Fatalf("replayed event = %+v; want the message so far", ev)
	}
	close(release)
	rest := collect(t, second)
	collect(t, first)
	var answer string
	for _, ev := range rest {
		if ev.Kind == domain.LibrarianAnswer {
			answer = ev.Text
		}
	}
	if answer != "half released" || rest[len(rest)-1].Kind != domain.LibrarianDone {
		t.Errorf("attached events = %+v; want the live answer, then done", rest)
	}
}

func TestAttach_MatchesOnlyTheAsk(t *testing.T) {
	t.Parallel()

	l := newTestLibrarian(t, answers(2), newFakePorts())
	ask := func(id string) {
		ch, err := l.Ask(context.Background(), domain.LibrarianAsk{ID: id, Conversation: "conv-1", Question: "q " + id})
		if err != nil {
			t.Fatalf("Ask(%s): %v", id, err)
		}
		collect(t, ch)
	}
	ask("ask-1")

	// A finished run still replays whole for a late reconnect.
	ch, ok := l.Attach(context.Background(), "conv-1", "ask-1")
	if !ok {
		t.Fatal("Attach refused the run that just finished")
	}
	if evs := collect(t, ch); !slices.ContainsFunc(evs, func(ev domain.LibrarianEvent) bool { return ev.Kind == domain.LibrarianAnswer }) {
		t.Errorf("replay = %+v; want the answer", evs)
	}

	for _, tt := range []struct{ conversation, id string }{
		{"conv-1", "other"},
		{"conv-2", "ask-1"},
		{"conv-1", ""},
	} {
		if _, ok := l.Attach(context.Background(), tt.conversation, tt.id); ok {
			t.Errorf("Attach(%q, %q) succeeded", tt.conversation, tt.id)
		}
	}
	ask("ask-2")
	if _, ok := l.Attach(context.Background(), "conv-1", "ask-1"); ok {
		t.Error("Attach reached a run the next ask replaced")
	}
}

// TestAcquire_PublishesACompleteRun: the slot never holds a run without its
// cancel, so a reconnect's grace timer can always end it.
func TestAcquire_PublishesACompleteRun(t *testing.T) {
	t.Parallel()

	l := newTestLibrarian(t, llmtest.NewScripted(), newFakePorts())
	conv, err := l.acquire(domain.LibrarianAsk{ID: "ask-1", Conversation: "conv-1", Question: "q"}, func() {})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if conv.run == nil || conv.run.cancel == nil {
		t.Errorf("published run = %+v; want its cancel set", conv.run)
	}
}
