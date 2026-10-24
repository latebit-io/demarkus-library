package librarian

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
// record, and the replay log its subscriptions read. Tools and the provider's
// retry narration write from nib's goroutines while the translator and the
// subscriptions read, hence the mutex.
type run struct {
	id       string
	question string
	started  time.Time
	grace    time.Duration
	cancel   context.CancelFunc // ends the run; set by Ask before it starts

	mu          sync.Mutex
	steps       []domain.LibrarianStep
	sources     []domain.LibrarianSource
	sentSources int // sources already published
	stopped     bool

	// log holds every published event but tokens; message is the message
	// streaming now, and messages counts completed ones so a subscription
	// knows when its offset into message went stale.
	log      []domain.LibrarianEvent
	message  strings.Builder
	messages int
	done     bool
	changed  chan struct{} // closed and replaced on every change
	attached int           // live subscriptions
	abandon  *time.Timer   // the grace countdown while nobody is attached
}

func newRun(id, question string, started time.Time, grace time.Duration) *run {
	return &run{id: id, question: question, started: started, grace: grace, changed: make(chan struct{})}
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

// trace records step and publishes it.
func (r *run) trace(step domain.LibrarianStep) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steps = append(r.steps, step)
	r.log = append(r.log, domain.LibrarianEvent{Kind: domain.LibrarianTrace, Text: step.Text, Ref: step.Ref})
	r.changedLocked()
}

// notice traces plain text; the provider's retry narration calls it.
func (r *run) notice(text string) {
	r.trace(domain.LibrarianStep{Text: text})
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

// publish hands ev to every subscription: a token extends the message in
// progress, anything else joins the log.
func (r *run) publish(ev domain.LibrarianEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ev.Kind == domain.LibrarianToken {
		r.message.WriteString(ev.Text)
	} else {
		r.log = append(r.log, ev)
	}
	r.changedLocked()
}

// completeMessage ends the message in progress: content, when non-empty, is
// its authoritative text and is logged as an answer.
func (r *run) completeMessage(content string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if content == "" && r.message.Len() == 0 {
		return // nothing streamed, nothing to settle
	}
	if content != "" {
		r.log = append(r.log, domain.LibrarianEvent{Kind: domain.LibrarianAnswer, Text: content})
	}
	r.message.Reset()
	r.messages++
	r.changedLocked()
}

// messageText is the message in progress, cloned: the transcript keeps it
// without pinning the builder's spare capacity.
func (r *run) messageText() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Clone(r.message.String())
}

// finish marks the run done: subscriptions deliver LibrarianDone and close.
func (r *run) finish() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.done = true
	r.stopAbandonLocked()
	r.changedLocked()
}

func (r *run) changedLocked() {
	close(r.changed)
	r.changed = make(chan struct{})
}

func (r *run) stopAbandonLocked() {
	if r.abandon != nil {
		r.abandon.Stop()
		r.abandon = nil
	}
}

// subscribe streams the run into a new channel: the log so far, the message
// in progress as one token, then live changes until done or ctx ends. Each
// subscription reads at its own pace, so none holds up the run.
func (r *run) subscribe(ctx context.Context) <-chan domain.LibrarianEvent {
	out := make(chan domain.LibrarianEvent, outBufferSize)
	r.attach()
	go func() {
		defer close(out)
		defer r.detach()
		var cursor, offset, messages int
		var batch []domain.LibrarianEvent
		for {
			r.mu.Lock()
			// Copied, never aliased: appending the tail must not write into
			// the log's spare capacity.
			batch = latestAnswerOnly(append(batch[:0], r.log[cursor:]...))
			cursor = len(r.log)
			if messages != r.messages {
				messages, offset = r.messages, 0
			}
			if text := r.message.String(); len(text) > offset {
				batch = append(batch, domain.LibrarianEvent{Kind: domain.LibrarianToken, Text: text[offset:]})
				offset = len(text)
			}
			done, changed := r.done, r.changed
			r.mu.Unlock()
			if done {
				batch = append(batch, domain.LibrarianEvent{Kind: domain.LibrarianDone})
			}
			for _, ev := range batch {
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				}
			}
			if done {
				return
			}
			select {
			case <-changed:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

// latestAnswerOnly drops each answer a later one in batch supersedes: the
// reader only ever sees the latest, so a replay renders it once.
func latestAnswerOnly(batch []domain.LibrarianEvent) []domain.LibrarianEvent {
	last := -1
	for i, ev := range batch {
		if ev.Kind == domain.LibrarianAnswer {
			last = i
		}
	}
	kept := batch[:0]
	for i, ev := range batch {
		if ev.Kind != domain.LibrarianAnswer || i == last {
			kept = append(kept, ev)
		}
	}
	return kept
}

// attach counts a subscription and calls off any abandon countdown.
func (r *run) attach() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attached++
	r.stopAbandonLocked()
}

// detach drops a subscription; with none left on a live run the grace
// countdown starts, ending the run unless someone attaches first.
func (r *run) detach() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attached--
	if r.attached > 0 || r.done {
		return
	}
	r.abandon = time.AfterFunc(r.grace, r.cancel)
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

// translate drains one run's nib events into its log and owns its end:
// AgentParked aborts (asks are one-shot), AgentEnd records the exchange and
// finish releases the subscriptions. ctx is the run's own, never a reader's.
func (l *Librarian) translate(ctx context.Context, conv *conversation, current *run) {
	defer current.cancel()
	defer l.release(conv, current) // frees the slot should the loop ever exit early

	var usage usageTally
	outcome := "answered"

	for ev := range conv.events {
		switch e := ev.(type) {
		case nibevent.MessageStart:
			current.completeMessage("") // whatever streamed before is over
		case nibevent.MessageUpdate:
			current.publish(domain.LibrarianEvent{Kind: domain.LibrarianToken, Text: e.Delta})
		case nibevent.MessageEnd:
			// The authoritative full message — deltas are droppable under
			// backpressure (nib's contract); consumers reconcile on this.
			current.completeMessage(e.Message.Content)
		case nibevent.ToolStart:
			current.trace(l.describe(e.Name, e.Args))
		case nibevent.ToolEnd:
			if e.IsError {
				current.trace(domain.LibrarianStep{Text: "⚠ " + firstLine(e.Result)})
			}
			for _, src := range current.unsentSources() {
				current.publish(domain.LibrarianEvent{Kind: domain.LibrarianSourceOpened, Text: src.Title, Ref: src.Ref})
			}
		case nibevent.TurnUsage:
			usage.add(e)
		case nibevent.MaxTurnsReached:
			outcome = "capped"
			current.trace(domain.LibrarianStep{Text: fmt.Sprintf("stopped at the turn cap (%d turns) — answering with what I have", e.Turns)})
		case nibevent.Error:
			outcome = "error"
			current.publish(domain.LibrarianEvent{Kind: domain.LibrarianError, Text: e.Err})
		case nibevent.AgentParked:
			// The answer is complete; the librarian doesn't hold parked
			// runs — unwind so AgentEnd delivers the transcript.
			conv.agent.Abort()
		case nibevent.AgentEnd:
			answer := finalAnswer(e.Messages)
			switch {
			case current.isStopped():
				outcome = "stopped"
				if partial := current.messageText(); partial != "" {
					answer = partial
				}
				current.trace(domain.LibrarianStep{Text: "stopped at your request"})
			case errors.Is(ctx.Err(), context.DeadlineExceeded):
				outcome = "timed out"
				current.trace(domain.LibrarianStep{Text: "stopped: this answer ran past its time limit"})
			case ctx.Err() != nil:
				outcome = "abandoned"
			case outcome == "capped":
				answer = l.finalWord(withRun(ctx, current), e.Messages, &usage)
			}
			l.mu.Lock()
			conv.exchanges = append(conv.exchanges, current.exchange(answer))
			conv.lastAskID = current.id
			l.releaseLocked(conv, current)
			l.mu.Unlock()
			l.cfg.Logger.Info("librarian ask",
				"conversation", conversationTag(conv.key), "outcome", outcome,
				"turns", usage.turns, "tool_calls", usage.toolCalls,
				"prompt_tokens", usage.prompt, "completion_tokens", usage.completion,
				"cached_tokens", usage.cached, "duration_ms", l.now().Sub(current.started).Milliseconds())
			current.finish()
			return
		}
	}
}

// capNudge asks a capped run for its answer.
const capNudge = "You have used your tool budget for this question. Answer now from what you found, citing what you opened, and say plainly what you could not find."

// finalWord makes the turn cap's promise good: one more model turn without
// tools, streamed like any answer, so a capped run never ends with a trace
// and no answer. Returns the answer, "" if the turn failed.
func (l *Librarian) finalWord(ctx context.Context, msgs []llm.Message, usage *usageTally) string {
	current := runFrom(ctx)
	msgs = append(slices.Clone(msgs), llm.Message{Role: "user", Content: capNudge})
	stream, err := l.cfg.Provider.Stream(ctx, msgs, nil)
	if err != nil {
		l.cfg.Logger.Warn("librarian final answer failed", "err", err)
		return ""
	}
	for ev := range stream {
		if ev.Done {
			usage.addTurn(ev.Usage)
		}
		if ev.Err != nil {
			l.cfg.Logger.Warn("librarian final answer failed", "err", ev.Err)
			continue // drain: the provider closes the channel after its terminal event
		}
		if ev.Token != "" {
			current.publish(domain.LibrarianEvent{Kind: domain.LibrarianToken, Text: ev.Token})
		}
	}
	answer := current.messageText()
	current.completeMessage(answer)
	return answer
}

// release frees the conversation's run slot if r still holds it.
func (l *Librarian) release(conv *conversation, r *run) {
	l.mu.Lock()
	l.releaseLocked(conv, r)
	l.mu.Unlock()
}

// releaseLocked frees the slot only if r still holds it: a later ask may
// already have taken the slot this run gave up.
func (l *Librarian) releaseLocked(conv *conversation, r *run) {
	if conv.run == r {
		conv.run = nil
	}
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
// key, which is the reader's session id.
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
