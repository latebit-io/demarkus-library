package port

import "context"

type readerIPKey struct{}

// WithReaderIP attaches the reader's client IP, as the inbound adapter
// trusts it, so outbound calls made on the reader's behalf can forward it.
func WithReaderIP(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, readerIPKey{}, ip)
}

// ReaderIP returns the IP set by WithReaderIP, or "" when none.
func ReaderIP(ctx context.Context) string {
	ip, _ := ctx.Value(readerIPKey{}).(string)
	return ip
}
