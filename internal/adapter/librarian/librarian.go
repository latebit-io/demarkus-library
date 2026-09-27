// Package librarian implements port.Librarian on the nib agent kit: the
// Phase 4 AI librarian (plans/phase-4-ai-librarian.md, nib addendum).
//
// The nib foundation agent owns the multi-turn LLM loop; this adapter
// composes it with tools that wrap the core's read-only inbound ports
// (Reader, Catalog, GraphService, MapService) — so every read the librarian
// makes carries the asking reader's ctx (their bearer in broker mode) and is
// transport-symmetric (plan D1). The core never imports nib; the web adapter
// never sees past port.Librarian.
package librarian

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
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
	// defaultHistoryExchanges is how many past exchanges ride into the next
	// ask: enough for a follow-up, few enough that cost stays flat.
	defaultHistoryExchanges = 6
	// defaultAsksPerHour caps one conversation's asks in a rolling hour.
	defaultAsksPerHour = 30
	// budgetWindow is the rolling window the ask budget counts over.
	budgetWindow = time.Hour
	// eventBufferSize is the nib events channel capacity per conversation.
	// The translator drains continuously during a run; the buffer just
	// absorbs token bursts so nib never drops MessageUpdate deltas casually.
	eventBufferSize = 128
	// outBufferSize is the domain-event channel capacity handed to the
	// caller (the SSE handler); sends select against ctx so a gone consumer
	// never wedges the translator.
	outBufferSize = 64
)

// Config assembles a Librarian. Provider and the four ports are required;
// zero-value caps take the package defaults.
type Config struct {
	// Provider is the nib LLM backend (resolved by the composition root via
	// nib's llmconfig — Anthropic, OpenAI-compatible, or local).
	Provider llm.Provider
	// Reader, Catalog, Graph, and Map are the read-only port slices the
	// tools wrap. The Editor slice is deliberately absent: the librarian
	// never writes.
	Reader  port.Reader
	Catalog port.Catalog
	Graph   port.GraphService
	Map     port.MapService
	// DefaultWorld is where world-less tool calls read, mirroring the
	// reading room's default-world routing.
	DefaultWorld string
	// Logger receives one usage line per ask; nil discards them.
	Logger *slog.Logger
	// MaxTurns caps LLM turns per ask (default defaultMaxTurns).
	MaxTurns int
	// MaxConversations bounds the conversation store (default
	// defaultMaxConversations).
	MaxConversations int
	// HistoryExchanges is how many past exchanges the model sees (default
	// defaultHistoryExchanges).
	HistoryExchanges int
	// AsksPerHour caps each conversation's asks (default defaultAsksPerHour).
	AsksPerHour int
}

// Librarian implements port.Librarian on a per-conversation nib agent.
type Librarian struct {
	cfg   Config
	tools []nibagent.Tool
	steps map[string]stepDescriber // tool name → its trace narration
	now   func() time.Time

	mu    sync.Mutex
	convs map[string]*conversation
}

// conversation is one reader's ongoing exchange: a dedicated nib agent (it
// serializes runs — the single-flight), its events channel, the transcript,
// and the ask times the budget counts.
type conversation struct {
	key       string
	agent     *nibagent.Agent
	events    chan nibevent.Event
	exchanges []domain.LibrarianExchange
	asks      []time.Time
	run       *run // the run in flight; nil when idle
	lastUsed  time.Time
}

// New validates cfg and builds the Librarian. The tool set is fixed at
// construction — the same read-only tools for every conversation.
func New(cfg Config) (*Librarian, error) {
	if cfg.Provider == nil {
		return nil, errors.New("librarian: Provider is required")
	}
	if cfg.Reader == nil || cfg.Catalog == nil || cfg.Graph == nil || cfg.Map == nil {
		return nil, errors.New("librarian: Reader, Catalog, Graph, and Map ports are required")
	}
	cfg.MaxTurns = positiveOr(cfg.MaxTurns, defaultMaxTurns)
	cfg.MaxConversations = positiveOr(cfg.MaxConversations, defaultMaxConversations)
	cfg.HistoryExchanges = positiveOr(cfg.HistoryExchanges, defaultHistoryExchanges)
	cfg.AsksPerHour = positiveOr(cfg.AsksPerHour, defaultAsksPerHour)
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	tools := []toolWithStep{
		&worldsTool{maps: cfg.Map},
		&findTool{reader: cfg.Reader},
		&lookupTool{catalog: cfg.Catalog},
		&openTool{reader: cfg.Reader, defaultWorld: cfg.DefaultWorld},
		&linksTool{graph: cfg.Graph, reader: cfg.Reader, defaultWorld: cfg.DefaultWorld},
		&versionsTool{catalog: cfg.Catalog, defaultWorld: cfg.DefaultWorld},
	}
	l := &Librarian{cfg: cfg, steps: map[string]stepDescriber{}, now: time.Now, convs: map[string]*conversation{}}
	for _, tool := range tools {
		l.tools = append(l.tools, tool)
		l.steps[tool.Definition().Function.Name] = tool
	}
	return l, nil
}

// positiveOr applies a cap default: zero or negative takes fallback.
func positiveOr(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

// Ask implements port.Librarian: one question, one run, one event stream.
func (l *Librarian) Ask(ctx context.Context, ask domain.LibrarianAsk) (<-chan domain.LibrarianEvent, error) {
	ask.Question = strings.TrimSpace(ask.Question)
	if ask.Question == "" {
		return nil, errors.New("librarian: empty question")
	}
	conv, err := l.acquire(ask.Conversation, ask.Question)
	if err != nil {
		return nil, err
	}

	l.mu.Lock()
	msgs := transcript(conv.exchanges, l.cfg.HistoryExchanges, ask)
	current := conv.run
	l.mu.Unlock()

	if err := conv.agent.PromptWithMessages(withRun(ctx, current), msgs); err != nil {
		l.release(conv, current)
		if errors.Is(err, nibagent.ErrRunInProgress) {
			return nil, domain.ErrLibrarianBusy
		}
		return nil, fmt.Errorf("librarian: prompt: %w", err)
	}
	out := make(chan domain.LibrarianEvent, outBufferSize)
	go l.translate(ctx, conv, out)
	return out, nil
}

// acquire returns the conversation for key with a fresh run for question in
// its slot, or ErrLibrarianBusy, or a *LibrarianLimitError past the hourly
// budget. Creating a conversation may evict the least-recently-used idle one.
func (l *Librarian) acquire(key, question string) (*conversation, error) {
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
			return nil, fmt.Errorf("librarian: new agent: %w", err)
		}
		conv = &conversation{key: key, agent: agent, events: events}
		l.convs[key] = conv
	}
	if conv.run != nil {
		return nil, domain.ErrLibrarianBusy
	}
	now := l.now()
	conv.asks = slices.DeleteFunc(conv.asks, func(at time.Time) bool { return now.Sub(at) >= budgetWindow })
	if len(conv.asks) >= l.cfg.AsksPerHour {
		return nil, &domain.LibrarianLimitError{RetryAfter: conv.asks[0].Add(budgetWindow).Sub(now)}
	}
	conv.asks = append(conv.asks, now)
	conv.run = newRun(question, now)
	conv.lastUsed = now
	return conv, nil
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
		if c.run != nil {
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

// History implements port.Librarian: a copy of the conversation's exchanges.
func (l *Librarian) History(conversation string) []domain.LibrarianExchange {
	l.mu.Lock()
	defer l.mu.Unlock()
	conv, ok := l.convs[conversation]
	if !ok {
		return nil
	}
	return slices.Clone(conv.exchanges)
}

// Stop implements port.Librarian: abort the run in flight. The translator
// sees the run unwind and records the stopped exchange.
func (l *Librarian) Stop(conversation string) {
	l.mu.Lock()
	conv, ok := l.convs[conversation]
	if !ok || conv.run == nil {
		l.mu.Unlock()
		return
	}
	conv.run.markStopped()
	agent := conv.agent
	l.mu.Unlock()
	agent.Abort()
}

// Reset implements port.Librarian: clear the transcript, keep the budget.
func (l *Librarian) Reset(conversation string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	conv, ok := l.convs[conversation]
	if !ok {
		return nil
	}
	if conv.run != nil {
		return domain.ErrLibrarianBusy
	}
	conv.exchanges = nil
	return nil
}
