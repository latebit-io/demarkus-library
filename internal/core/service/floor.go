package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// floorSatellites caps how many top-importance documents each world cluster
// shows. The catalog returns importance order, so the cap keeps exactly the
// most load-bearing documents (hubs, architecture, decisions per the
// tagging conventions) as labeled satellites.
const floorSatellites = 10

// floorCache holds the last assembled floor. One universe per deployment,
// so a single slot under a mutex — the focused-live policy decides when it
// refreshes, the cache only remembers.
type floorCache struct {
	mu    sync.Mutex
	floor *domain.Floor
}

func (f *floorCache) get() (domain.Floor, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.floor == nil {
		return domain.Floor{}, false
	}
	return *f.floor, true
}

func (f *floorCache) put(floor domain.Floor) {
	f.mu.Lock()
	f.floor = &floor
	f.mu.Unlock()
}

// Floor assembles the universe view's data live: the gateway's world list
// (mark_worlds in broker mode — the permission mask; the home world over
// QUIC), then each world's whole-catalog lookup ("*", importance order). A
// world whose catalog read fails still appears, satellite-less and marked
// (an old server that rejects match-all, an unreachable world): on the
// floor, absence would read as nonexistence.
func (s *ReadingService) Floor(ctx context.Context) (domain.Floor, error) {
	worlds, err := s.world.Worlds(ctx)
	if err != nil {
		return domain.Floor{}, err
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
	s.floor.put(floor)
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
