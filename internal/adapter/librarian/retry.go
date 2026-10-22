package librarian

// nib providers surface a throttled request as a plain "api error: status
// 429" from Stream and never retry; under company rate limits that is a
// routine failure, so the librarian backs off and tries again, visibly.

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"time"

	"github.com/latebit-io/nib/ai/llm"
)

const (
	// defaultRateLimitRetries is how many times a throttled turn is retried.
	defaultRateLimitRetries = 4
	// rateLimitBackoff is the first wait; it doubles per retry (2, 4, 8, 16s).
	rateLimitBackoff = 2 * time.Second
)

// statusPattern finds the HTTP status in a nib provider error message; nib
// gives no typed error for it.
var statusPattern = regexp.MustCompile(`\bstatus (\d{3})\b`)

// retryingProvider wraps the LLM provider with a backoff on throttling.
type retryingProvider struct {
	inner   llm.Provider
	retries int
	logger  *slog.Logger
	sleep   func(ctx context.Context, d time.Duration) error
}

func newRetryingProvider(inner llm.Provider, retries int, logger *slog.Logger) *retryingProvider {
	return &retryingProvider{inner: inner, retries: retries, logger: logger, sleep: sleepCtx}
}

// Stream retries a throttled call with exponential backoff. The wait is
// narrated into the run in flight (a trace line) and aborts with ctx.
func (p *retryingProvider) Stream(ctx context.Context, msgs []llm.Message, tools []llm.ToolDef) (<-chan llm.StreamEvent, error) {
	for attempt := 0; ; attempt++ {
		ch, err := p.inner.Stream(ctx, msgs, tools)
		if err == nil || attempt >= p.retries || !isThrottled(err) {
			return ch, err
		}
		wait := rateLimitBackoff << attempt
		p.logger.Warn("librarian provider throttled", "attempt", attempt+1, "retries", p.retries, "wait", wait, "err", err)
		if r := runFrom(ctx); r != nil {
			r.notice(fmt.Sprintf("the model provider is rate limiting — waiting %s before trying again (%d of %d)", wait, attempt+1, p.retries))
		}
		if err := p.sleep(ctx, wait); err != nil {
			return nil, err
		}
	}
}

// isThrottled reports a 429 (rate limited) or 529 (Anthropic overloaded):
// the statuses a short wait can cure.
func isThrottled(err error) bool {
	m := statusPattern.FindStringSubmatch(err.Error())
	if m == nil {
		return false
	}
	status, _ := strconv.Atoi(m[1])
	return status == 429 || status == 529
}

// sleepCtx waits d or until ctx ends, returning ctx's error in that case.
func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
