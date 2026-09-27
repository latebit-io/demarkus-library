package librarian

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
	nibevent "github.com/latebit-io/nib/agent/event"
	"github.com/latebit-io/nib/ai/llm"
)

// run is one ask's working state: the trace and sources its exchange will
// record. Tools add sources from nib's loop goroutine while the translator
// reads them, hence the mutex.
type run struct {
	question string
	started  time.Time

	mu          sync.Mutex
	steps       []domain.LibrarianStep
	sources     []domain.LibrarianSource
	sentSources int // sources already emitted as events
	stopped     bool
}

func newRun(question string, started time.Time) *run {
	return &run{question: question, started: started}
}

type runKey struct{}

// withRun carries the run to the tools: nib hands each Execute the run's ctx.
func withRun(ctx context.Context, r *run) context.Context {
	return context.WithValue(ctx, runKey{}, r)
}

// runFrom returns the run a tool call belongs to; nil outside a run.
func runFrom(ctx context.Context) *run {
	r, _ := ctx.Value(runKey{}).(*run)
	return r
}

func (r *run) markStopped() {
	r.mu.Lock()
	r.stopped = true
	r.mu.Unlock()
}

func (r *run) isStopped() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopped
}

func (r *run) addStep(step domain.LibrarianStep) {
	r.mu.Lock()
	r.steps = append(r.steps, step)
	r.mu.Unlock()
}

// addSource records an opened document once, however often it was opened.
func (r *run) addSource(src domain.LibrarianSource) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, have := range r.sources {
		if have.Ref == src.Ref {
			return
		}
	}
	r.sources = append(r.sources, src)
}

// unsentSources returns the sources recorded since the last call.
func (r *run) unsentSources() []domain.LibrarianSource {
	r.mu.Lock()
	defer r.mu.Unlock()
	fresh := r.sources[r.sentSources:]
	r.sentSources = len(r.sources)
	return fresh
}

// exchange is the run as the transcript keeps it, owning its own slices.
func (r *run) exchange(answer string) domain.LibrarianExchange {
	r.mu.Lock()
	defer r.mu.Unlock()
	return domain.LibrarianExchange{
		Question: r.question,
		Answer:   answer,
		Steps:    slices.Clone(r.steps),
		Sources:  slices.Clone(r.sources),
		Stopped:  r.stopped,
	}
}

// usageTally sums one run's provider-reported usage for the log line.
type usageTally struct {
	turns, toolCalls, prompt, completion, cached int
}

// addTurn counts a turn made outside nib's loop (the cap's final word).
func (u *usageTally) addTurn(usage *llm.Usage) {
	u.turns++
	if usage != nil {
		u.prompt += usage.PromptTokens
		u.completion += usage.CompletionTokens
		u.cached += usage.CachedTokens
	}
}

func (u *usageTally) add(e nibevent.TurnUsage) {
	u.turns++
	u.toolCalls += e.ToolCalls
	u.prompt += e.PromptTokens
	u.completion += e.CompletionTokens
	u.cached += e.CachedTokens
}

// translate drains one run's nib events into the domain vocabulary. It owns
// the run's end-of-life: nib parks a finished turn awaiting a reply — the
// librarian's asks are one-shot, so AgentParked triggers Abort, AgentEnd
// yields the transcript the exchange is taken from, and the out channel
// closes after done. On ctx cancellation (the reader left) emits become
// drops but the drain continues to AgentEnd, keeping nib's channel moving.
func (l *Librarian) translate(ctx context.Context, conv *conversation, out chan<- domain.LibrarianEvent) {
	defer close(out)
	l.mu.Lock()
	current := conv.run
	l.mu.Unlock()
	defer l.release(conv, current)

	emit := func(ev domain.LibrarianEvent) {
		select {
		case out <- ev:
		case <-ctx.Done():
		}
	}
	var usage usageTally
	var streamed strings.Builder // the message in progress: the answer if the reader stops mid-message
	outcome := "answered"

	for ev := range conv.events {
		switch e := ev.(type) {
		case nibevent.MessageStart:
			streamed.Reset()
		case nibevent.MessageUpdate:
			streamed.WriteString(e.Delta)
			emit(domain.LibrarianEvent{Kind: domain.LibrarianToken, Text: e.Delta})
		case nibevent.MessageEnd:
			// The authoritative full message — deltas are droppable under
			// backpressure (nib's contract); consumers reconcile on this.
			if e.Message.Content != "" {
				emit(domain.LibrarianEvent{Kind: domain.LibrarianAnswer, Text: e.Message.Content})
			}
		case nibevent.ToolStart:
			step := l.describe(e.Name, e.Args)
			current.addStep(step)
			emit(domain.LibrarianEvent{Kind: domain.LibrarianTrace, Text: step.Text, Ref: step.Ref})
		case nibevent.ToolEnd:
			if e.IsError {
				step := domain.LibrarianStep{Text: "⚠ " + firstLine(e.Result)}
				current.addStep(step)
				emit(domain.LibrarianEvent{Kind: domain.LibrarianTrace, Text: step.Text})
			}
			for _, src := range current.unsentSources() {
				emit(domain.LibrarianEvent{Kind: domain.LibrarianSourceOpened, Text: src.Title, Ref: src.Ref})
			}
		case nibevent.TurnUsage:
			usage.add(e)
		case nibevent.MaxTurnsReached:
			outcome = "capped"
			step := domain.LibrarianStep{Text: fmt.Sprintf("stopped at the turn cap (%d turns) — answering with what I have", e.Turns)}
			current.addStep(step)
			emit(domain.LibrarianEvent{Kind: domain.LibrarianTrace, Text: step.Text})
		case nibevent.Error:
			outcome = "error"
			emit(domain.LibrarianEvent{Kind: domain.LibrarianError, Text: e.Err})
		case nibevent.AgentParked:
			// The answer is complete; the librarian doesn't hold parked
			// runs — unwind so AgentEnd delivers the transcript.
			conv.agent.Abort()
		case nibevent.AgentEnd:
			answer := finalAnswer(e.Messages)
			switch {
			case current.isStopped():
				outcome = "stopped"
				if streamed.Len() > 0 {
					answer = streamed.String()
				}
				step := domain.LibrarianStep{Text: "stopped at your request"}
				current.addStep(step)
				emit(domain.LibrarianEvent{Kind: domain.LibrarianTrace, Text: step.Text})
			case ctx.Err() != nil:
				outcome = "abandoned"
			case outcome == "capped":
				answer = l.finalWord(ctx, e.Messages, emit, &usage)
			}
			l.mu.Lock()
			conv.exchanges = append(conv.exchanges, current.exchange(answer))
			l.mu.Unlock()
			l.cfg.Logger.Info("librarian ask",
				"conversation", conversationTag(conv.key), "outcome", outcome,
				"turns", usage.turns, "tool_calls", usage.toolCalls,
				"prompt_tokens", usage.prompt, "completion_tokens", usage.completion,
				"cached_tokens", usage.cached, "duration_ms", l.now().Sub(current.started).Milliseconds())
			emit(domain.LibrarianEvent{Kind: domain.LibrarianDone})
			return
		}
	}
}

// capNudge asks a capped run for its answer.
const capNudge = "You have used your tool budget for this question. Answer now from what you found, citing what you opened, and say plainly what you could not find."

// finalWord makes the turn cap's promise good: one more model turn without
// tools, streamed like any answer, so a capped run never ends with a trace
// and no answer. Returns the answer, "" if the turn failed.
func (l *Librarian) finalWord(ctx context.Context, msgs []llm.Message, emit func(domain.LibrarianEvent), usage *usageTally) string {
	msgs = append(slices.Clone(msgs), llm.Message{Role: "user", Content: capNudge})
	stream, err := l.cfg.Provider.Stream(ctx, msgs, nil)
	if err != nil {
		l.cfg.Logger.Warn("librarian final answer failed", "err", err)
		return ""
	}
	var answer strings.Builder
	for ev := range stream {
		if ev.Done {
			usage.addTurn(ev.Usage)
		}
		if ev.Err != nil {
			l.cfg.Logger.Warn("librarian final answer failed", "err", ev.Err)
			continue // drain: the provider closes the channel after its terminal event
		}
		if ev.Token != "" {
			answer.WriteString(ev.Token)
			emit(domain.LibrarianEvent{Kind: domain.LibrarianToken, Text: ev.Token})
		}
	}
	if answer.Len() > 0 {
		emit(domain.LibrarianEvent{Kind: domain.LibrarianAnswer, Text: answer.String()})
	}
	return answer.String()
}

// release frees the conversation's run slot, but only if r still holds it: a
// later ask may already have taken the slot this run gave up.
func (l *Librarian) release(conv *conversation, r *run) {
	l.mu.Lock()
	if conv.run == r {
		conv.run = nil
	}
	l.mu.Unlock()
}

// finalAnswer is the last non-empty assistant message after the question:
// intermediate assistant turns narrate tool use and are superseded.
func finalAnswer(msgs []llm.Message) string {
	answer := ""
	for _, m := range msgs {
		switch {
		case m.Role == "user":
			answer = ""
		case m.Role == "assistant" && m.Content != "":
			answer = m.Content
		}
	}
	return answer
}

// conversationTag correlates a conversation's log lines without logging its
// key, which is the reader's session cookie.
func conversationTag(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:6])
}

// firstLine truncates multi-line tool errors to their first line for the
// margin-sized trace.
func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
