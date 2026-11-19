package broker

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/latebit-io/demarkus-library/internal/adapter/bearer"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus-library/internal/core/port"
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

// A token refresh keys a new pool entry. It starts with the world list of the
// reader whose key rode in on the earlier read, so the watches can renew on it
// before any list runs as it. No other reader's list is taken.
func TestARefreshedBearerStartsWithItsReadersWorlds(t *testing.T) {
	srv := newMCPTestServer(t)
	g := srv.gateway()
	defer g.Close()
	expires := time.Now().Add(time.Minute)
	reader := port.WithReader(bearer.WithExpiry(bearer.WithToken(t.Context(), "good-token"), expires), "reader-1")
	if _, err := g.Fetch(reader, "soul", "/index.md"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if _, err := g.ReaderWorlds(t.Context()); err != nil {
		t.Fatalf("ReaderWorlds: %v", err)
	}

	// The test broker knows one bearer, so entryFor stands in for the reads
	// that bring the next ones. A wrongly taken list would win on expiry.
	pool := g.caller.(*mcpCaller)
	pool.entryFor("fresh-token", expires.Add(15*time.Minute), "reader-1")
	pool.entryFor("other-token", expires.Add(20*time.Minute), "reader-2")
	pool.entryFor("keyless-token", expires.Add(25*time.Minute), "")
	got, ok := lenderFor(pool.lenders(), "soul")
	if !ok || got.token != "fresh-token" {
		t.Errorf("lent %q (ok %v), want the reader's refreshed bearer", got.token, ok)
	}
}

// A list that showed no worlds is the reader's latest word: the next bearer
// starts with none, not with an older entry's worlds.
func TestAnEmptyWorldListIsInheritedOverAnOlderOne(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	caller := testCaller(now, map[string]poolEntry{
		"older": {now.Add(time.Minute), []string{"team-a"}},
		"newer": {now.Add(10 * time.Minute), []string{}},
	})
	caller.pool["older"].reader, caller.pool["newer"].reader = "reader-1", "reader-1"

	fresh := caller.entryFor("fresh", now.Add(25*time.Minute), "reader-1")
	if fresh.worlds == nil || len(fresh.worlds) != 0 {
		t.Errorf("worlds = %v, want listed and empty", fresh.worlds)
	}
}

// The pool's cap may evict the reader's only other entry as the new one
// arrives; its world list is read first.
func TestInheritanceSurvivesEvictingTheDonor(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	caller := testCaller(now, map[string]poolEntry{"donor": {now.Add(time.Minute), []string{"team-a"}}})
	caller.pool["donor"].reader = "reader-1"
	caller.pool["donor"].lastUsed = now.Add(-time.Minute)
	for i := range maxPool - 1 {
		caller.pool[fmt.Sprintf("bearer-%d", i)] = &pooledEntry{lastUsed: now}
	}

	fresh := caller.entryFor("fresh", now.Add(15*time.Minute), "reader-1")
	if _, kept := caller.pool["donor"]; kept {
		t.Fatal("the donor was not evicted, so nothing is shown")
	}
	if !slices.Equal(fresh.worlds, []string{"team-a"}) {
		t.Errorf("worlds = %v, want the evicted donor's [team-a]", fresh.worlds)
	}
}

// A refreshed bearer the broker refuses loses the list it inherited, so the
// watches keep to a bearer that works.
func TestARefusedRefreshedBearerIsNotLent(t *testing.T) {
	srv := newMCPTestServer(t)
	g := srv.gateway()
	defer g.Close()
	expires := time.Now().Add(time.Minute)
	reader := port.WithReader(bearer.WithExpiry(bearer.WithToken(t.Context(), "good-token"), expires), "reader-1")
	if _, err := g.Fetch(reader, "soul", "/index.md"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if _, err := g.ReaderWorlds(t.Context()); err != nil {
		t.Fatalf("ReaderWorlds: %v", err)
	}

	refused := port.WithReader(bearer.WithExpiry(bearer.WithToken(t.Context(), "bad-token"), expires.Add(15*time.Minute)), "reader-1")
	if _, err := g.Fetch(refused, "soul", "/index.md"); err == nil {
		t.Fatal("Fetch on a refused bearer succeeded")
	}
	got, ok := lenderFor(g.caller.(*mcpCaller).lenders(), "soul")
	if !ok || got.token != "good-token" {
		t.Errorf("lent %q (ok %v), want the bearer the broker accepts", got.token, ok)
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
	// Listed and empty, not unlisted: the reader's next bearer inherits this.
	if worlds := g.caller.(*mcpCaller).pool["revoked-token"].worlds; worlds == nil || len(worlds) != 0 {
		t.Errorf("worlds = %v, want listed and empty", worlds)
	}
}
