package port

import "context"

type readerKey struct{}

// WithReader marks ctx with an opaque key for the signed-in reader (the web
// session id), so the core can keep per-reader state — what a reader may see
// is a property of their identity, never shared across identities.
func WithReader(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, readerKey{}, key)
}

// ReaderKey returns the key set by WithReader, or "" for an anonymous request
// (direct-QUIC mode, or a route outside the turnstile).
func ReaderKey(ctx context.Context) string {
	key, _ := ctx.Value(readerKey{}).(string)
	return key
}
