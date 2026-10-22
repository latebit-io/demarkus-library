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

// throttledProvider fails its first failures Stream calls with the given
// error, then delegates.
type throttledProvider struct {
	inner    llm.Provider
	err      error
	failures int
	calls    atomic.Int32
}

func (p *throttledProvider) Stream(ctx context.Context, msgs []llm.Message, tools []llm.ToolDef) (<-chan llm.StreamEvent, error) {
	if int(p.calls.Add(1)) <= p.failures {
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

func TestAsk_RetriesThrottledTurn(t *testing.T) {
	t.Parallel()

	scripted := &scriptedProvider{turns: [][]llm.StreamEvent{textTurn("fine now")}}
	throttled := &throttledProvider{inner: scripted, err: errThrottled, failures: 2}
	l := newTestLibrarian(t, throttled, newFakePorts())
	var mu sync.Mutex
	var waits []time.Duration
	l.cfg.Provider.(*retryingProvider).sleep = func(_ context.Context, d time.Duration) error {
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

func TestRetry_GivesUpPastRetries(t *testing.T) {
	t.Parallel()

	throttled := &throttledProvider{inner: &scriptedProvider{}, err: errThrottled, failures: 10}
	_, err := quietRetrying(throttled, 2).Stream(context.Background(), nil, nil)
	if !errors.Is(err, errThrottled) {
		t.Errorf("err = %v; want the throttle error surfaced", err)
	}
	if got := throttled.calls.Load(); got != 3 {
		t.Errorf("provider calls = %d; want 1 + 2 retries", got)
	}
}

func TestRetry_OtherStatusesNotRetried(t *testing.T) {
	t.Parallel()

	bad := errors.New("api error: status 400: invalid request")
	throttled := &throttledProvider{inner: &scriptedProvider{}, err: bad, failures: 10}
	_, err := quietRetrying(throttled, 3).Stream(context.Background(), nil, nil)
	if !errors.Is(err, bad) {
		t.Errorf("err = %v; want the 400 surfaced", err)
	}
	if got := throttled.calls.Load(); got != 1 {
		t.Errorf("provider calls = %d; want 1", got)
	}
}

func TestRetry_CancelDuringWait(t *testing.T) {
	t.Parallel()

	throttled := &throttledProvider{inner: &scriptedProvider{}, err: errThrottled, failures: 10}
	p := newRetryingProvider(throttled, 3, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.Stream(ctx, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v; want context.Canceled from the wait", err)
	}
}
