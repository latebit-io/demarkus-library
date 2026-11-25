package librarian

// nib's llm.WithRetry owns the backoff and decides what is worth retrying;
// this file only sets the policy and narrates each wait into its run.

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/latebit-io/nib/ai/llm"
)

const (
	// retryAttempts is the Stream calls one turn may take, the first included.
	retryAttempts = 5
	// retryBaseDelay is the first wait; nib doubles it per retry (2, 4, 8, 16s).
	retryBaseDelay = 2 * time.Second
	// retryMaxDelay caps one wait: a provider asking for longer through
	// Retry-After fails the turn instead of holding the reader.
	retryMaxDelay = 30 * time.Second
)

// withNarratedRetry retries a temporary provider failure (llm.IsTemporary)
// and traces each wait into the run whose turn is waiting.
func withNarratedRetry(inner llm.Provider, logger *slog.Logger) llm.Provider {
	const retries = retryAttempts - 1
	return llm.WithRetry(inner, llm.RetryPolicy{
		MaxAttempts: retryAttempts,
		BaseDelay:   retryBaseDelay,
		MaxDelay:    retryMaxDelay,
		OnRetry: func(ctx context.Context, attempt int, wait time.Duration, err error) {
			logger.Warn("librarian provider busy", "attempt", attempt, "retries", retries, "wait", wait, "err", err)
			if r := runFrom(ctx); r != nil {
				r.notice(fmt.Sprintf("the model provider is busy — waiting %s before trying again (%d of %d)", wait, attempt, retries))
			}
		},
	})
}
