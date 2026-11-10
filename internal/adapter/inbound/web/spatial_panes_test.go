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
	panes := spatialPanes{terms: DefaultTerms(), identity: worldIdentity{brands: NewWorldBrands(raws)}}
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
