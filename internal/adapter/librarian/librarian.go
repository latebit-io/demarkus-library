// Package librarian implements port.Librarian on the nib agent kit: the
// Phase 4 AI librarian (plans/phase-4-ai-librarian.md, nib addendum).
//
// The nib foundation agent owns the multi-turn LLM loop; this adapter
// composes it with tools that wrap the core's read-only inbound ports
// (Reader, GraphService, MapService) — so every read the librarian makes
// carries the asking reader's ctx (their bearer in broker mode) and is
// transport-symmetric (plan D1). The core never imports nib; the web
// adapter never sees past port.Librarian.
package librarian

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus-library/internal/core/port"
	nibagent "github.com/latebit-io/nib/agent"
	nibevent "github.com/latebit-io/nib/agent/event"
	"github.com/latebit-io/nib/ai/llm"
)

const (
	// defaultMaxTurns caps LLM turns per ask (plan D7) via nib's
	// Options.MaxTurns — the guard against tool-call ping-pong.
	defaultMaxTurns = 15
	// defaultMaxConversations LRU-bounds the per-process conversation store
	// (the linkGraph pattern: fine at replicaCount 1, Tier-4 concern later).
	defaultMaxConversations = 256
	// eventBufferSize is the nib events channel capacity per conversation.
	// The translator drains continuously during a run; the buffer just
	// absorbs token bursts so nib never drops MessageUpdate deltas casually.
	eventBufferSize = 128
	// outBufferSize is the domain-event channel capacity handed to the
	// caller (the SSE handler); sends select against ctx so a gone consumer
	// never wedges the translator.
	outBufferSize = 64
)

// systemPrompt is the librarian's persona and tool doctrine. Stable text —
// per-ask context (the trail, later) belongs in the user turn, not here.
const systemPrompt = `You are the librarian of a demarkus universe — a
versioned, distributed catalog of markdown documents organized into worlds.
Readers ask you questions; you answer by working the catalog with your tools,
never from assumption:

- worlds: orient — the authorized worlds and each one's most important documents.
- find: locate documents by title or path (name match, not full-text search).
- open: read a document's source and catalog metadata.
- links: trace a document's outbound links and observed backlinks.

Ground every claim in something you opened. Cite documents inline as markdown
links — [title](mark://<world>/<path>) — so the reader can click through and
follow you; a bare address is not a citation. When the catalog does not
answer the question, say so plainly — never invent holdings. Be concise:
answer first, brief support after.`

// Config assembles a Librarian. Provider, Reader, Graph, and Map are
// required; zero-value caps take the package defaults.
type Config struct {
	// Provider is the nib LLM backend (resolved by the composition root via
	// nib's llmconfig — Anthropic, OpenAI-compatible, or local).
	Provider llm.Provider
	// Reader, Graph, and Map are the read-only port slices the tools wrap.
	// The Editor slice is deliberately absent: the librarian never writes.
	Reader port.Reader
	Graph  port.GraphService
	Map    port.MapService
	// DefaultWorld is where world-less tool calls read, mirroring the
	// reading room's default-world routing.
	DefaultWorld string
	// MaxTurns caps LLM turns per ask (default defaultMaxTurns).
	MaxTurns int
	// MaxConversations bounds the conversation store (default
	// defaultMaxConversations).
	MaxConversations int
}

// Librarian implements port.Librarian on a per-conversation nib agent.
type Librarian struct {
	cfg   Config
	tools []nibagent.Tool

	mu    sync.Mutex
	convs map[string]*conversation
}

// conversation is one reader's ongoing exchange: a dedicated nib agent (it
// serializes runs — the single-flight), its events channel, and the saved
// transcript replayed into the next ask.
type conversation struct {
	agent    *nibagent.Agent
	events   chan nibevent.Event
	history  []llm.Message
	inFlight bool
	lastUsed time.Time
}

// New validates cfg and builds the Librarian. The tool set is fixed at
// construction — the same four read-only tools for every conversation.
func New(cfg Config) (*Librarian, error) {
	if cfg.Provider == nil {
		return nil, errors.New("librarian: Provider is required")
	}
	if cfg.Reader == nil || cfg.Graph == nil || cfg.Map == nil {
		return nil, errors.New("librarian: Reader, Graph, and Map ports are required")
	}
	if cfg.MaxTurns == 0 {
		cfg.MaxTurns = defaultMaxTurns
	}
	if cfg.MaxConversations == 0 {
		cfg.MaxConversations = defaultMaxConversations
	}
	return &Librarian{
		cfg: cfg,
		tools: []nibagent.Tool{
			&worldsTool{maps: cfg.Map},
			&findTool{reader: cfg.Reader},
			&openTool{reader: cfg.Reader, defaultWorld: cfg.DefaultWorld},
			&linksTool{graph: cfg.Graph, defaultWorld: cfg.DefaultWorld},
		},
		convs: make(map[string]*conversation),
	}, nil
}

// Ask implements port.Librarian: one question, one run, one event stream.
func (l *Librarian) Ask(ctx context.Context, conversation, question string) (<-chan domain.LibrarianEvent, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return nil, errors.New("librarian: empty question")
	}

	conv, err := l.acquire(conversation)
	if err != nil {
		return nil, err
	}

	// Transcript: saved history (which already leads with the system
	// message) plus the new user turn; a fresh conversation starts one.
	msgs := make([]llm.Message, 0, len(conv.history)+2)
	if len(conv.history) == 0 {
		msgs = append(msgs, llm.Message{Role: "system", Content: systemPrompt})
	} else {
		msgs = append(msgs, conv.history...)
	}
	msgs = append(msgs, llm.Message{Role: "user", Content: question})

	if err := conv.agent.PromptWithMessages(ctx, msgs); err != nil {
		l.release(conv)
		if errors.Is(err, nibagent.ErrRunInProgress) {
			return nil, domain.ErrLibrarianBusy
		}
		return nil, fmt.Errorf("librarian: %w", err)
	}

	out := make(chan domain.LibrarianEvent, outBufferSize)
	go l.translate(ctx, conv, out)
	return out, nil
}

// acquire returns the conversation for key with its in-flight flag taken, or
// ErrLibrarianBusy. Creating a conversation may evict the least-recently-used
// idle one past the cap.
func (l *Librarian) acquire(key string) (*conversation, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	conv, ok := l.convs[key]
	if !ok {
		l.evictLocked()
		events := make(chan nibevent.Event, eventBufferSize)
		agent, err := nibagent.New(nibagent.Options{
			Provider: l.cfg.Provider,
			Events:   events,
			Tools:    l.tools,
			MaxTurns: l.cfg.MaxTurns,
		})
		if err != nil {
			return nil, fmt.Errorf("librarian: %w", err)
		}
		conv = &conversation{agent: agent, events: events}
		l.convs[key] = conv
	}
	if conv.inFlight {
		return nil, domain.ErrLibrarianBusy
	}
	conv.inFlight = true
	conv.lastUsed = time.Now()
	return conv, nil
}

// release clears a conversation's in-flight flag.
func (l *Librarian) release(conv *conversation) {
	l.mu.Lock()
	conv.inFlight = false
	l.mu.Unlock()
}

// evictLocked drops the least-recently-used idle conversation once the store
// is at capacity. In-flight conversations are never evicted; if every slot is
// mid-run (pathological), the store temporarily exceeds the cap rather than
// severing a live stream.
func (l *Librarian) evictLocked() {
	if len(l.convs) < l.cfg.MaxConversations {
		return
	}
	var oldestKey string
	var oldest time.Time
	for k, c := range l.convs {
		if c.inFlight {
			continue
		}
		if oldestKey == "" || c.lastUsed.Before(oldest) {
			oldestKey, oldest = k, c.lastUsed
		}
	}
	if oldestKey != "" {
		delete(l.convs, oldestKey)
	}
}

// translate drains one run's nib events into the domain vocabulary. It owns
// the run's end-of-life: nib parks a finished turn awaiting a reply — the
// librarian's asks are one-shot, so AgentParked triggers Abort, AgentEnd
// yields the final transcript to save, and the out channel closes after
// done/error. On ctx cancellation (the reader left) emits become drops but
// the drain continues to AgentEnd, keeping nib's event channel from wedging.
func (l *Librarian) translate(ctx context.Context, conv *conversation, out chan<- domain.LibrarianEvent) {
	defer close(out)
	defer l.release(conv)

	emit := func(ev domain.LibrarianEvent) {
		select {
		case out <- ev:
		case <-ctx.Done():
		}
	}

	for ev := range conv.events {
		switch e := ev.(type) {
		case nibevent.MessageUpdate:
			emit(domain.LibrarianEvent{Kind: domain.LibrarianToken, Text: e.Delta})
		case nibevent.MessageEnd:
			// The authoritative full message — deltas are droppable under
			// backpressure (nib's contract); consumers reconcile on this.
			if e.Message.Content != "" {
				emit(domain.LibrarianEvent{Kind: domain.LibrarianAnswer, Text: e.Message.Content})
			}
		case nibevent.ToolStart:
			emit(domain.LibrarianEvent{Kind: domain.LibrarianTrace, Text: traceLine(e.Name, e.Args)})
		case nibevent.ToolEnd:
			if e.IsError {
				emit(domain.LibrarianEvent{Kind: domain.LibrarianTrace, Text: "⚠ " + firstLine(e.Result)})
			}
		case nibevent.MaxTurnsReached:
			emit(domain.LibrarianEvent{Kind: domain.LibrarianTrace,
				Text: fmt.Sprintf("stopped at the turn cap (%d turns) — answering with what I have", e.Turns)})
		case nibevent.Error:
			emit(domain.LibrarianEvent{Kind: domain.LibrarianError, Text: e.Err})
		case nibevent.AgentParked:
			// The answer is complete; the librarian doesn't hold parked
			// runs — unwind so AgentEnd delivers the transcript.
			conv.agent.Abort()
		case nibevent.AgentEnd:
			l.mu.Lock()
			conv.history = e.Messages
			l.mu.Unlock()
			emit(domain.LibrarianEvent{Kind: domain.LibrarianDone})
			return
		}
	}
}

// History implements port.Librarian: the conversation's completed exchanges,
// derived from the saved transcript. Each user turn opens an exchange; the
// last non-empty assistant content before the next user turn is its answer
// (intermediate assistant turns narrate tool use and are superseded).
func (l *Librarian) History(conversation string) []domain.LibrarianExchange {
	l.mu.Lock()
	conv, ok := l.convs[conversation]
	var history []llm.Message
	if ok {
		history = conv.history
	}
	l.mu.Unlock()

	var out []domain.LibrarianExchange
	for _, m := range history {
		switch m.Role {
		case "user":
			out = append(out, domain.LibrarianExchange{Question: m.Content})
		case "assistant":
			if m.Content != "" && len(out) > 0 {
				out[len(out)-1].Answer = m.Content
			}
		}
	}
	return out
}

// traceLine renders one tool call as a single legible trace line:
// `find {"query":"deploy"}` → `find query="deploy"`.
func traceLine(name, args string) string {
	fields := compactArgs(args)
	if fields == "" {
		return name
	}
	return name + " " + fields
}

// firstLine truncates multi-line tool errors to their first line for the
// margin-sized trace.
func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
