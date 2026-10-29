package broker

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/latebit-io/demarkus-library/internal/adapter/bearer"
)

// The lent bearer is the one that lapses last; one about to lapse, or of
// unknown expiry, is never lent.
func TestFreshestLendsTheLongestLivedBearer(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		pool     map[string]time.Time
		want     string
		wantNone bool
	}{
		{name: "longest lived", pool: map[string]time.Time{
			"a": now.Add(5 * time.Minute), "b": now.Add(14 * time.Minute), "c": now.Add(time.Minute),
		}, want: "b"},
		{name: "about to lapse", pool: map[string]time.Time{"a": now.Add(lendMinLife - time.Second)}, wantNone: true},
		{name: "unknown expiry", pool: map[string]time.Time{"a": {}}, wantNone: true},
		{name: "empty room", pool: map[string]time.Time{}, wantNone: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			caller := &mcpCaller{now: func() time.Time { return now }, pool: map[string]*pooledEntry{}}
			for token, expires := range tt.pool {
				caller.pool[token] = &pooledEntry{expires: expires, lastUsed: now}
			}
			got, ok := caller.freshest()
			if ok == tt.wantNone {
				t.Fatalf("ok = %v, want %v", ok, !tt.wantNone)
			}
			if ok && (got.token != tt.want || !got.expires.Equal(tt.pool[tt.want])) {
				t.Errorf("lent %q until %v, want %q until %v", got.token, got.expires, tt.want, tt.pool[tt.want])
			}
		})
	}
}

// A reader's read stamps the bearer's expiry on its entry; the watch then
// borrows that reader's live session, and lists worlds as that reader.
func TestWatchSourceLendsASignedInReadersSession(t *testing.T) {
	srv := newMCPTestServer(t)
	g := srv.gateway()
	defer g.Close()

	if _, err := g.WatchSource(t.Context()); !errors.Is(err, bearer.ErrNoReader) {
		t.Fatalf("empty room: err = %v, want ErrNoReader", err)
	}

	expires := time.Now().Add(10 * time.Minute)
	reader := bearer.WithExpiry(bearer.WithToken(t.Context(), "good-token"), expires)
	if _, err := g.Fetch(reader, "soul", "/index.md"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	session, err := g.WatchSource(t.Context())
	if err != nil {
		t.Fatalf("WatchSource: %v", err)
	}
	if session.Listener == nil || !session.Expires.Equal(expires) {
		t.Errorf("session = %+v, want a listener until %v", session, expires)
	}
	if n := srv.handshakeCount(); n != 1 {
		t.Errorf("handshake count = %d, want 1 (the reader's session reused)", n)
	}

	names, err := g.ReaderWorlds(t.Context())
	if err != nil {
		t.Fatalf("ReaderWorlds: %v", err)
	}
	if !slices.Equal(names, []string{"root", "soul"}) {
		t.Errorf("worlds = %v, want [root soul]", names)
	}
}
