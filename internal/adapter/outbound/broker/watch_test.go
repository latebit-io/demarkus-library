package broker

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/latebit-io/demarkus-library/internal/adapter/bearer"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// poolEntry is one test pool slot: the bearer's expiry and what its reader sees.
type poolEntry struct {
	expires time.Time
	worlds  []string
}

func testCaller(now time.Time, pool map[string]poolEntry) *mcpCaller {
	caller := &mcpCaller{now: func() time.Time { return now }, pool: map[string]*pooledEntry{}}
	for token, e := range pool {
		caller.pool[token] = &pooledEntry{expires: e.expires, worlds: e.worlds, lastUsed: now}
	}
	return caller
}

// A world's watch borrows the bearer, of the readers who see that world, that
// lapses last; one about to lapse, or of unknown expiry, is never lent.
func TestLenderForLendsTheLongestLivedReaderOfTheWorld(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		pool     map[string]poolEntry
		want     string
		wantNone bool
	}{
		{name: "longest lived", pool: map[string]poolEntry{
			"a": {now.Add(5 * time.Minute), []string{"team-a"}},
			"b": {now.Add(14 * time.Minute), []string{"root", "team-a"}},
			"c": {now.Add(time.Minute), []string{"team-a"}},
		}, want: "b"},
		{name: "a longer life that cannot see the world", pool: map[string]poolEntry{
			"a": {now.Add(5 * time.Minute), []string{"root", "team-a"}},
			"b": {now.Add(14 * time.Minute), []string{"root"}},
		}, want: "a"},
		{name: "worlds not yet listed", pool: map[string]poolEntry{"a": {now.Add(5 * time.Minute), nil}}, wantNone: true},
		{name: "about to lapse", pool: map[string]poolEntry{"a": {now.Add(lendMinLife - time.Second), []string{"team-a"}}}, wantNone: true},
		{name: "unknown expiry", pool: map[string]poolEntry{"a": {time.Time{}, []string{"team-a"}}}, wantNone: true},
		{name: "empty room", pool: map[string]poolEntry{}, wantNone: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := lenderFor(testCaller(now, tt.pool).lenders(), "team-a")
			if ok == tt.wantNone {
				t.Fatalf("ok = %v, want %v", ok, !tt.wantNone)
			}
			if ok && (got.token != tt.want || !got.expires.Equal(tt.pool[tt.want].expires)) {
				t.Errorf("lent %q until %v, want %q until %v", got.token, got.expires, tt.want, tt.pool[tt.want].expires)
			}
		})
	}
}

// The pod watches every world some lendable reader sees, not one reader's.
func TestSeenWorldsIsEveryLendersWorlds(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	caller := testCaller(now, map[string]poolEntry{
		"a": {now.Add(5 * time.Minute), []string{"root", "team-a"}},
		"b": {now.Add(14 * time.Minute), []string{"root", "soul"}},
		"c": {now.Add(time.Second), []string{"lapsing"}},
	})
	if got := seenWorlds(caller.lenders()); !slices.Equal(got, []string{"root", "soul", "team-a"}) {
		t.Errorf("worlds = %v, want [root soul team-a]", got)
	}
}

// A reader's read stamps the bearer's expiry on its entry; listing worlds as
// that reader then lends its live session to the worlds it sees, and no other.
func TestWorldSourceLendsAReaderWhoSeesTheWorld(t *testing.T) {
	srv := newMCPTestServer(t)
	g := srv.gateway()
	defer g.Close()

	if _, err := g.ReaderWorlds(t.Context()); !errors.Is(err, bearer.ErrNoReader) {
		t.Fatalf("empty room: err = %v, want ErrNoReader", err)
	}
	expires := time.Now().Add(10 * time.Minute)
	reader := bearer.WithExpiry(bearer.WithToken(t.Context(), "good-token"), expires)
	if _, err := g.Fetch(reader, "soul", "/index.md"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if _, err := g.WorldSource("soul")(t.Context()); !errors.Is(err, bearer.ErrNoReader) {
		t.Fatalf("before a list: err = %v, want ErrNoReader", err)
	}

	names, err := g.ReaderWorlds(t.Context())
	if err != nil {
		t.Fatalf("ReaderWorlds: %v", err)
	}
	if !slices.Equal(names, []string{"root", "soul"}) {
		t.Errorf("worlds = %v, want [root soul]", names)
	}
	session, err := g.WorldSource("soul")(t.Context())
	if err != nil {
		t.Fatalf("WorldSource: %v", err)
	}
	if session.Listener == nil || !session.Expires.Equal(expires) {
		t.Errorf("session = %+v, want a listener until %v", session, expires)
	}
	if _, err := g.WorldSource("team-a")(t.Context()); !errors.Is(err, bearer.ErrNoReader) {
		t.Errorf("unseen world: err = %v, want ErrNoReader", err)
	}
	if n := srv.handshakeCount(); n != 1 {
		t.Errorf("handshake count = %d, want 1 (the reader's session reused)", n)
	}
}

// A refused world list forgets what the bearer saw, so a revoked reader stops
// being lent at the next list.
func TestRefusedWorldListForgetsTheLender(t *testing.T) {
	srv := newMCPTestServer(t)
	g := srv.gateway()
	defer g.Close()
	expires := time.Now().Add(10 * time.Minute)
	g.caller.(*mcpCaller).pool["revoked-token"] = &pooledEntry{expires: expires, worlds: []string{"soul"}, lastUsed: time.Now()}

	_, err := g.ReaderWorlds(t.Context())
	if !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("ReaderWorlds: err = %v, want ErrUnauthorized", err)
	}
	if _, err := g.WorldSource("soul")(t.Context()); !errors.Is(err, bearer.ErrNoReader) {
		t.Errorf("revoked reader still lent: err = %v", err)
	}
}
