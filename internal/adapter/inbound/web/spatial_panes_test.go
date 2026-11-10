package web

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// gatedRaws holds every branding read until want of them are in flight, so
// a caller that reads one world at a time stalls into its deadline.
type gatedRaws struct {
	want     int32
	inFlight atomic.Int32
	open     chan struct{}
	once     sync.Once
}

func (g *gatedRaws) Raw(ctx context.Context, _, path string) (domain.RawDocument, error) {
	if g.inFlight.Add(1) == g.want {
		g.once.Do(func() { close(g.open) })
	}
	select {
	case <-g.open:
	case <-ctx.Done():
		return domain.RawDocument{}, ctx.Err()
	}
	return brandedDoc(path)
}

// brandedDoc is a world that names itself and carries a logo, and nothing else.
func brandedDoc(path string) (domain.RawDocument, error) {
	switch path {
	case WorldBrandDoc:
		return domain.RawDocument{Body: "# Branding\n\nIdentity.\n\n```yaml\nname: Room\n```\n"}, nil
	case WorldBrandLogo:
		return domain.RawDocument{Body: "# Logo\n\nThe mark.\n\n```svg\n<svg xmlns='http://www.w3.org/2000/svg'/>\n```\n"}, nil
	default:
		return domain.RawDocument{}, domain.ErrNotFound
	}
}

// A cold cache reads every world's brand side by side; portals and
// unreadable worlds are never read.
func TestFloorDrawReadsBrandsConcurrently(t *testing.T) {
	t.Parallel()
	const readable = 4
	raws := &gatedRaws{want: readable, open: make(chan struct{})}
	panes := spatialPanes{terms: DefaultTerms(), identity: worldIdentity{brands: NewWorldBrands(raws)},
		logoReads: make(chan struct{}, floorLogoReads)}
	var floor domain.Floor
	for i := range readable {
		floor.Worlds = append(floor.Worlds, domain.FloorWorld{World: domain.WorldInfo{Name: fmt.Sprintf("w%d", i)}})
	}
	floor.Worlds = append(floor.Worlds,
		domain.FloorWorld{World: domain.WorldInfo{Name: "portal"}, Portal: true},
		domain.FloorWorld{World: domain.WorldInfo{Name: "down"}, Err: true})

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	draw := panes.floorDraw(ctx, floor, trail{Panes: []paneAddr{{Kind: paneFloor}}}, 0)
	if len(draw.logos) != readable {
		t.Fatalf("logos = %v, want one per readable world (reads ran one at a time?)", draw.logos)
	}
	for _, skipped := range []string{"portal", "down"} {
		if _, ok := draw.logos[skipped]; ok {
			t.Errorf("%s drew a logo; it must not be read", skipped)
		}
	}
}

// brandMode is how brandRaws answers one world.
type brandMode int

const (
	brandLogo  brandMode = iota // names itself and carries a logo
	brandBare                   // names itself, no logo
	brandStuck                  // holds the read until freed, whatever its context says, as a broker session lock does
)

// brandRaws serves each world's branding per its mode and counts the reads.
type brandRaws struct {
	mu    sync.Mutex
	modes map[string]brandMode
	free  chan struct{}
	reads atomic.Int32
}

func (r *brandRaws) set(world string, mode brandMode) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.modes[world] = mode
}

func (r *brandRaws) Raw(_ context.Context, world, path string) (domain.RawDocument, error) {
	r.reads.Add(1)
	r.mu.Lock()
	mode := r.modes[world]
	r.mu.Unlock()
	switch {
	case mode == brandStuck:
		<-r.free
	case mode == brandBare && path == WorldBrandLogo:
		return domain.RawDocument{}, domain.ErrNotFound
	}
	return brandedDoc(path)
}

// floorOf is a universe of plain worlds.
func floorOf(worlds ...string) domain.Floor {
	var floor domain.Floor
	for _, w := range worlds {
		floor.Worlds = append(floor.Worlds, domain.FloorWorld{World: domain.WorldInfo{Name: w}})
	}
	return floor
}

// At the deadline each world draws from what it knows: a fresh answer wins,
// even one without a logo, over the cached brand; a world still reading keeps
// its cached brand or manifest logo, and one with neither draws as a lobe.
func TestFloorDrawStopsAtDeadline(t *testing.T) {
	t.Parallel()
	raws := &brandRaws{modes: map[string]brandMode{}, free: make(chan struct{})}
	t.Cleanup(func() { close(raws.free) })
	brands := NewWorldBrands(raws)
	for _, w := range []string{"dropped", "stuck-cached"} {
		if _, ok := brands.For(t.Context(), w); !ok {
			t.Fatalf("warming %s found no brand", w)
		}
	}
	brands.ttl = 0 // every brand stale, so each draw reads
	raws.set("dropped", brandBare)
	for _, w := range []string{"stuck-cached", "stuck-filed", "stuck-bare"} {
		raws.set(w, brandStuck)
	}
	panes := spatialPanes{terms: DefaultTerms(), logoReads: make(chan struct{}, floorLogoReads), identity: worldIdentity{
		manifest: map[string]WorldBranding{"stuck-filed": {LogoURL: "/theme/worlds/stuck-filed/logo"}},
		brands:   brands,
	}}

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	draw := panes.floorDraw(ctx, floorOf("fresh", "dropped", "stuck-cached", "stuck-filed", "stuck-bare"), trail{Panes: []paneAddr{{Kind: paneFloor}}}, 0)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("floorDraw waited %v on stuck reads", elapsed)
	}
	for _, tt := range []struct {
		world, want string
	}{
		{world: "fresh", want: "/theme/worlds/fresh/logo"},
		{world: "dropped", want: ""}, // answered without one: the cached logo is stale
		{world: "stuck-cached", want: "/theme/worlds/stuck-cached/logo"},
		{world: "stuck-filed", want: "/theme/worlds/stuck-filed/logo"},
		{world: "stuck-bare", want: ""},
	} {
		t.Run(tt.world, func(t *testing.T) {
			if got := draw.logos[tt.world]; got != tt.want {
				t.Errorf("logo = %q, want %q", got, tt.want)
			}
		})
	}
}

// Reads stuck past one render keep their slots, so the next render waits on
// the room's limit instead of starting more.
func TestFloorDrawStuckReadsCapTheRoom(t *testing.T) {
	t.Parallel()
	raws := &brandRaws{modes: map[string]brandMode{"a": brandStuck, "b": brandStuck, "c": brandStuck}, free: make(chan struct{})}
	t.Cleanup(func() { close(raws.free) })
	panes := spatialPanes{terms: DefaultTerms(), identity: worldIdentity{brands: NewWorldBrands(raws)},
		logoReads: make(chan struct{}, 2)}
	for range 3 {
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		panes.floorDraw(ctx, floorOf("a", "b", "c"), trail{Panes: []paneAddr{{Kind: paneFloor}}}, 0)
		cancel()
	}
	if n := raws.reads.Load(); n != 2 {
		t.Errorf("%d reads started across three renders, want the room's 2", n)
	}
}

// Answers already in when the deadline fires count, however select breaks
// the tie between them and the deadline.
func TestGatherLogosDrainsAtDeadline(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for range 50 {
		answers := make(chan worldLogo, 3)
		answers <- worldLogo{world: "a", logo: "/a"}
		answers <- worldLogo{world: "b"}
		got := gatherLogos(ctx, answers, 3)
		if want := map[string]string{"a": "/a", "b": ""}; fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("answered = %v, want %v", got, want)
		}
	}
}
