package service

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// floorSatellites caps how many top-importance documents each world cluster
// shows. The catalog returns importance order, so the cap keeps exactly the
// most load-bearing documents (hubs, architecture, decisions per the
// tagging conventions) as labeled satellites.
const floorSatellites = 10

// floorTTL bounds how long a freshly built floor is reused before a focused
// rebuild. The floor is the heaviest read in the room — it fans out a world
// list + a per-world catalog lookup + the hub graph fetch, several broker
// requests that the per-subject rate budget is not sized for — and the
// focused-live policy would otherwise rebuild it on every focus and every
// refresh. A short window collapses repeated views to one rebuild while
// keeping worlds/topology current (they move on the order of minutes; the
// agent re-crawls hourly).
const floorTTL = 30 * time.Second

// floorCache holds the last assembled floor. One universe per deployment,
// so a single slot under a mutex — getFresh gates the focused rebuild on
// floorTTL, get always returns the last value (the stale-fallback path).
type floorCache struct {
	mu       sync.Mutex
	floor    *domain.Floor
	storedAt time.Time
	now      func() time.Time // nil ⇒ time.Now (overridable in tests)
}

func (f *floorCache) clockNow() time.Time {
	if f.now != nil {
		return f.now()
	}
	return time.Now()
}

func (f *floorCache) get() (domain.Floor, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.floor == nil {
		return domain.Floor{}, false
	}
	return *f.floor, true
}

// getFresh returns the cached floor only when it is younger than ttl — the
// focused path skips its broker fan-out within the window.
func (f *floorCache) getFresh(ttl time.Duration) (domain.Floor, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.floor == nil || f.clockNow().Sub(f.storedAt) >= ttl {
		return domain.Floor{}, false
	}
	return *f.floor, true
}

func (f *floorCache) put(floor domain.Floor) {
	f.mu.Lock()
	f.floor = &floor
	f.storedAt = f.clockNow()
	f.mu.Unlock()
}

// Floor returns the universe view, rebuilding live only when the cached floor
// has aged past floorTTL. The focused-live policy still applies — a focused
// floor is "live" — but live here means "no older than the TTL window," which
// keeps the floor's heavy broker fan-out from firing on every focus/refresh
// and overrunning the per-subject rate budget. A transient gateway failure
// (a rate-limit blip, a flapping world) falls back to the last-good floor
// rather than erroring the whole page; ErrUnauthorized is exempt — the
// reader's identity dying must surface as a re-login, never a stale render.
func (s *ReadingService) Floor(ctx context.Context) (domain.Floor, error) {
	if floor, ok := s.floor.getFresh(floorTTL); ok {
		return floor, nil
	}
	floor, err := s.buildFloor(ctx)
	if err != nil {
		if !errors.Is(err, domain.ErrUnauthorized) {
			if stale, ok := s.floor.get(); ok {
				// Serve stale, but never silently — a persistent rebuild
				// failure (not just a rate-limit blip) would otherwise hide
				// behind a frozen floor with no operator signal.
				slog.Warn("floor: serving stale cache after rebuild failed", "err", err)
				return stale, nil
			}
		}
		return domain.Floor{}, err
	}
	s.floor.put(floor)
	return floor, nil
}

// buildFloor assembles the universe view's data live: the gateway's world list
// (mark_worlds in broker mode — the permission mask; the home world over
// QUIC), then each world's whole-catalog lookup ("*", importance order). A
// world whose catalog read fails still appears, satellite-less and marked
// (an old server that rejects match-all, an unreachable world): on the
// floor, absence would read as nonexistence.
func (s *ReadingService) buildFloor(ctx context.Context) (domain.Floor, error) {
	worlds, err := s.world.Worlds(ctx)
	if err != nil {
		return domain.Floor{}, err
	}

	// Authorization + addressing baseline (the permission mask): which worlds
	// the reader may see and the host→name map that joins host-keyed topology
	// back to authorized names.
	authorized := make(map[string]bool, len(worlds))
	host2name := make(map[string]string, len(worlds))
	for _, w := range worlds {
		authorized[w.Name] = true
		// Join key is the world's dial Address (what the topology graph keys
		// its nodes by); fall back to URL for older brokers / QUIC mode.
		joinAddr := w.Address
		if joinAddr == "" {
			joinAddr = w.URL
		}
		if h := hostOf(joinAddr); h != "" {
			host2name[h] = w.Name
		}
	}

	floor := domain.Floor{Worlds: make([]domain.FloorWorld, 0, len(worlds))}
	for _, w := range worlds {
		fw := domain.FloorWorld{World: w}
		raw, err := s.world.Lookup(ctx, w.Name, "/", "*", "")
		switch {
		case errors.Is(err, domain.ErrUnauthorized):
			// The reader's identity died mid-assembly — that is the
			// page's problem (re-login), not one world's.
			return domain.Floor{}, err
		case err != nil:
			fw.Err = true
		default:
			fw.Docs = parseCatalogTable(raw.Body, floorSatellites)
		}
		floor.Worlds = append(floor.Worlds, fw)
	}

	// No worlds visible to this identity ⇒ stop with an empty floor (the web
	// layer renders the "no worlds visible to your identity" state). Skip the
	// topology enrichment entirely: drawing the hub graph's portal nodes for a
	// reader authorized for zero worlds would leak world names — and other
	// worlds' internal dial addresses — to an unauthorized identity, and render
	// a misleading map instead of the honest empty state.
	if len(floor.Worlds) == 0 {
		return floor, nil
	}

	// Topology enrichment (decision 11): the durable hub graph export unioned
	// with the R3 observed-links map, aggregated to world-level edges and
	// masked by the authorized set. Portal worlds are externally-linked hosts
	// with no authorized name (the extensional universe; ADR 0005 §16). All of
	// this degrades to nothing when no hub is published and nothing observed.
	hub := s.readHub(ctx, s.hub)
	edges, portals := worldEdges(append(hub.edges, s.graph.allEdges()...), host2name, authorized)
	floor.Edges = edges
	for _, p := range portals {
		floor.Worlds = append(floor.Worlds, domain.FloorWorld{
			World: domain.WorldInfo{Name: p, URL: "mark://" + p}, Portal: true,
		})
	}

	return floor, nil
}

// FloorCached serves the last assembled floor, building live when none
// exists yet. Unfocused floor panes read here — same focused-live policy
// as every other pane.
func (s *ReadingService) FloorCached(ctx context.Context) (domain.Floor, error) {
	if floor, ok := s.floor.get(); ok {
		return floor, nil
	}
	return s.Floor(ctx)
}

// parseCatalogTable extracts FloorDocs from a LOOKUP result body — the
// markdown table both transports return (| Path | Importance | Title |
// Tags |). The table is machine-shaped by construction (buildLookupResults
// escapes cell content), so a light parse is honest: header/separator rows
// drop by shape, malformed rows drop silently, and the status: tag axis
// resolves each doc's badge exactly like the document margin does.
func parseCatalogTable(body string, limit int) []domain.FloorDoc {
	var out []domain.FloorDoc
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) < 4 {
			continue
		}
		path := unescapeMD(strings.TrimSpace(cells[0]))
		if path == "" || path == "Path" || strings.HasPrefix(path, "-") || !strings.HasPrefix(path, "/") {
			continue
		}
		importance, _ := strconv.ParseFloat(strings.TrimSpace(cells[1]), 64)
		title := unescapeMD(strings.TrimSpace(cells[2]))
		if title == "" {
			title = strings.TrimSuffix(path[strings.LastIndex(path, "/")+1:], ".md")
		}
		tags := splitTags(unescapeMD(strings.TrimSpace(cells[3])))
		out = append(out, domain.FloorDoc{
			Path:       path,
			Title:      title,
			Importance: importance,
			Status:     resolveStatus(tags, nil),
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

// unescapeMD reverses the lookup table's markdown escaping (backslash
// before punctuation) so titles and paths read clean.
func unescapeMD(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
