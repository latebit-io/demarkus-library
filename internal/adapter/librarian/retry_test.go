package librarian

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/nib/ai/llm"
)

// throttledProvider fails failures consecutive Stream calls with err,
// starting at call failFrom (1-based; zero means the first), then delegates.
type throttledProvider struct {
	inner    llm.Provider
	err      error
	failures int32
	failFrom int32
	calls    atomic.Int32
}

func (p *throttledProvider) Stream(ctx context.Context, msgs []llm.Message, tools []llm.ToolDef) (<-chan llm.StreamEvent, error) {
	call := p.calls.Add(1)
	from := max(p.failFrom, 1)
	if call >= from && call < from+p.failures {
		return nil, p.err
	}
	return p.inner.Stream(ctx, msgs, tools)
}

var errThrottled = errors.New("api error: status 429: {\"type\":\"rate_limit_error\"}")

func quietRetrying(inner llm.Provider, retries int) *retryingProvider {
	p := newRetryingProvider(inner, retries, slog.New(slog.NewTextHandler(io.Discard, nil)))
	p.sleep = func(context.Context, time.Duration) error { return nil }
	return p
}

// retrier reaches the retry wrapper New wrapped around the test provider.
func retrier(l *Librarian) *retryingProvider {
	return l.cfg.Provider.(*retryingProvider)
}

func TestAsk_RetriesThrottledTurn(t *testing.T) {
	t.Parallel()

	scripted := &scriptedProvider{turns: [][]llm.StreamEvent{textTurn("fine now")}}
	throttled := &throttledProvider{inner: scripted, err: errThrottled, failures: 2}
	l := newTestLibrarian(t, throttled, newFakePorts())
	var mu sync.Mutex
	var waits []time.Duration
	retrier(l).sleep = func(_ context.Context, d time.Duration) error {
		mu.Lock()
		waits = append(waits, d)
		mu.Unlock()
		return nil
	}

	ch, err := l.Ask(context.Background(), domain.LibrarianAsk{Conversation: "k", Question: "q"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	evs := collect(t, ch)

	notices := 0
	for _, ev := range evs {
		if ev.Kind == domain.LibrarianTrace && strings.Contains(ev.Text, "rate limiting") {
			notices++
		}
	}
	if notices != 2 {
		t.Errorf("rate-limit trace lines = %d; want 2 in %+v", notices, evs)
	}
	if last := evs[len(evs)-1]; last.Kind != domain.LibrarianDone {
		t.Errorf("stream ended with %v; want done", last.Kind)
	}
	if got := throttled.calls.Load(); got != 3 {
		t.Errorf("provider calls = %d; want 3 (two throttled, one served)", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(waits) != 2 || waits[0] != 2*time.Second || waits[1] != 4*time.Second {
		t.Errorf("waits = %v; want doubling from 2s", waits)
	}
	// The waits are part of the recorded work.
	hist := l.History("k")
	if len(hist) != 1 || len(hist[0].Steps) != 2 {
		t.Errorf("history steps = %+v; want the two retry notices", hist)
	}
}

func TestAsk_FinalWordRetryNoticeReachesStream(t *testing.T) {
	t.Parallel()

	// The cap's final word runs outside nib's loop while translate waits on
	// it; a retry notice queued there must still reach the stream, ahead
	// of the answer and done.
	scripted := &scriptedProvider{turns: [][]llm.StreamEvent{
		toolTurn("c1", "worlds", "{}"),
		textTurn("the floor."),
	}}
	// Throttle only the final word: call 1 is the capped turn.
	throttled := &throttledProvider{inner: scripted, err: errThrottled, failures: 2, failFrom: 2}
	ports := newFakePorts()
	l, err := New(Config{
		Provider: throttled, Reader: ports, Catalog: ports, Graph: ports, Map: ports,
		DefaultWorld: "root", MaxTurns: 1,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	retrier(l).sleep = func(context.Context, time.Duration) error { return nil }
	ch, err := l.Ask(context.Background(), domain.LibrarianAsk{Conversation: "k", Question: "q"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	evs := collect(t, ch)

	notice, answer := -1, -1
	for i, ev := range evs {
		switch {
		case ev.Kind == domain.LibrarianTrace && strings.Contains(ev.Text, "rate limiting") && notice < 0:
			notice = i
		case ev.Kind == domain.LibrarianAnswer:
			answer = i
		}
	}
	if notice < 0 || answer < 0 || notice > answer {
		t.Errorf("notice at %d, answer at %d; want the notice streamed before the answer: %+v", notice, answer, evs)
	}
	if last := evs[len(evs)-1]; last.Kind != domain.LibrarianDone {
		t.Errorf("stream ended with %v; want done", last.Kind)
	}
}

func TestRetry_StatusMatrix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		err       error
		wantCalls int32
	}{
		{name: "429 rate limited retries", err: errThrottled, wantCalls: 3},
		{name: "529 overloaded retries", err: errors.New("api error: status 529: overloaded"), wantCalls: 3},
		{name: "400 is permanent", err: errors.New("api error: status 400: invalid request"), wantCalls: 1},
		{name: "500 is not retried", err: errors.New("api error: status 500: boom"), wantCalls: 1},
		{name: "no status is not retried", err: errors.New("http request: dial tcp: refused"), wantCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			throttled := &throttledProvider{inner: &scriptedProvider{}, err: tt.err, failures: 10}
			_, err := quietRetrying(throttled, 2).Stream(context.Background(), nil, nil)
			if !errors.Is(err, tt.err) {
				t.Errorf("err = %v; want the provider error surfaced", err)
			}
			if got := throttled.calls.Load(); got != tt.wantCalls {
				t.Errorf("provider calls = %d; want %d", got, tt.wantCalls)
			}
		})
	}
}

func TestRetry_CancelDuringWait(t *testing.T) {
	t.Parallel()

	throttled := &throttledProvider{inner: &scriptedProvider{}, err: errThrottled, failures: 10}
	p := quietRetrying(throttled, 3)
	p.sleep = sleepCtx
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.Stream(ctx, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v; want context.Canceled from the wait", err)
	}
}
