package service

import (
	"context"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus-library/internal/core/port"
)

// errOf drops a read's value so each table row fits on one line.
func errOf[T any](_ T, err error) error { return err }

// TestCachesNeverServeAnotherReader: every per-pod cache is built under one
// reader's permission mask. Reader A's repeat read stays cached; reader B, who
// may see fewer worlds, always reads live instead of being served A's value.
func TestCachesNeverServeAnotherReader(t *testing.T) {
	type read func(svc *ReadingService, ctx context.Context) error
	tests := []struct {
		name          string
		prime, cached read
	}{
		{
			name:   "floor",
			prime:  func(svc *ReadingService, ctx context.Context) error { return errOf(svc.Floor(ctx)) },
			cached: func(svc *ReadingService, ctx context.Context) error { return errOf(svc.FloorCached(ctx)) },
		},
		{
			name:   "world map",
			prime:  func(svc *ReadingService, ctx context.Context) error { return errOf(svc.WorldMap(ctx, "team-a")) },
			cached: func(svc *ReadingService, ctx context.Context) error { return errOf(svc.WorldMapCached(ctx, "team-a")) },
		},
		{
			name:   "name index",
			prime:  func(svc *ReadingService, ctx context.Context) error { return errOf(svc.NameIndexCached(ctx, "team-a")) },
			cached: func(svc *ReadingService, ctx context.Context) error { return errOf(svc.NameIndexCached(ctx, "team-a")) },
		},
		{
			name:  "document",
			prime: func(svc *ReadingService, ctx context.Context) error { return errOf(svc.Read(ctx, "team-a", "/x.md")) },
			cached: func(svc *ReadingService, ctx context.Context) error {
				return errOf(svc.ReadCached(ctx, "team-a", "/x.md"))
			},
		},
		{
			name:  "tag page",
			prime: func(svc *ReadingService, ctx context.Context) error { return errOf(svc.Tag(ctx, "team-a", "adr")) },
			cached: func(svc *ReadingService, ctx context.Context) error {
				return errOf(svc.TagCached(ctx, "team-a", "adr"))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var called string
			gw := fakeGateway{
				called: &called,
				worlds: []domain.WorldInfo{{Name: "team-a"}},
				raw:    domain.RawDocument{Path: "/x.md", Body: lookupTable},
			}
			svc := NewReadingService(gw, fakeRenderer{html: "x"}, newFakeCache())
			readerA := port.WithReader(t.Context(), "session-a")
			readerB := port.WithReader(t.Context(), "session-b")

			if err := tt.prime(svc, readerA); err != nil {
				t.Fatalf("prime as reader A: %v", err)
			}
			called = ""
			if err := tt.cached(svc, readerA); err != nil {
				t.Fatalf("cached read as reader A: %v", err)
			}
			if called != "" {
				t.Errorf("reader A's repeat read hit gateway.%s; want the cache", called)
			}
			if err := tt.cached(svc, readerB); err != nil {
				t.Fatalf("cached read as reader B: %v", err)
			}
			if called == "" {
				t.Error("reader B was served reader A's cached value")
			}
		})
	}
}
