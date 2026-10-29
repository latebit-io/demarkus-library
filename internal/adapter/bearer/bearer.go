// Package bearer carries the logged-in reader's broker token through the
// request context — the contract between the web auth middleware (which puts
// the session's id_token in) and the broker world gateway (which sends it as
// the Authorization bearer on /mcp calls).
//
// It is a leaf utility deliberately outside both adapters: the inbound web
// adapter and the outbound broker adapter both import it without depending on
// each other, and the core ports stay free of auth vocabulary — they see only
// context.Context.
package bearer

import (
	"context"
	"errors"
	"time"
)

// ErrNoReader reports that no signed-in reader's bearer is there to act as:
// in broker mode, the normal state of an empty room.
var ErrNoReader = errors.New("no signed-in reader")

// Unexported key types, so only this package can mint the context keys.
type (
	ctxKey    struct{}
	expiryKey struct{}
)

// WithToken returns a context carrying the reader's bearer token.
func WithToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, ctxKey{}, token)
}

// FromContext extracts the bearer token, or "" when the request is
// unauthenticated (direct-QUIC mode, or a route outside the turnstile).
func FromContext(ctx context.Context) string {
	t, _ := ctx.Value(ctxKey{}).(string)
	return t
}

// WithExpiry records when the context's bearer stops being accepted, so a
// watch opened on it can renew in time.
func WithExpiry(ctx context.Context, expires time.Time) context.Context {
	return context.WithValue(ctx, expiryKey{}, expires)
}

// ExpiryFromContext returns the bearer's expiry, or zero when unknown.
func ExpiryFromContext(ctx context.Context) time.Time {
	t, _ := ctx.Value(expiryKey{}).(time.Time)
	return t
}
