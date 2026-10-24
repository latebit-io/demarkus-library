package service

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// floorSample caps the per-world catalog read on the floor. The map draws
// worlds as aggregates sized by this count, so it is a count proxy, not a
// satellite list; big worlds saturate at the cap.
const floorSample = 200

// floorTTL bounds how long a freshly built floor is reused before a focused
// rebuild. The floor is the heaviest read in the room — it fans out a world
// list + a per-world catalog lookup + the hub graph fetch, several broker
// requests that the per-subject rate budget is not sized for — and the
// focused-live policy would otherwise rebuild it on every focus and every
// refresh. A short window collapses repeated views to one rebuild while
// keeping worlds/topology current (they move on the order of minutes; the
// agent re-crawls hourly).
const floorTTL = 30 * time.Second

// floorRetain bounds how long a reader's last floor serves the stale fallback
// and unfocused panes. The floor is the reader's permission mask, so it is
// cached per reader and must age out.
const floorRetain = 10 * time.Minute

// Floor returns the universe view, rebuilding live only when the cached floor
// has aged past floorTTL. The focused-live policy still applies — a focused
// floor is "live" — but live here means "no older than the TTL window," which
// keeps the floor's heavy broker fan-out from firing on every focus/refresh
// and overrunning the per-subject rate budget. A transient gateway failure
// (a rate-limit blip, a flapping world) falls back to the last-good floor
// rather than erroring the whole page; ErrUnauthorized is exempt — the
// reader's identity dying must surface as a re-login, never a stale render.
func (s *ReadingService) Floor(ctx context.Context) (domain.Floor, error) {
	if floor, ok := s.floor.getFresh(ctx, universe, floorTTL); ok {
		return floor, nil
	}
	epoch := s.floor.epoch(universe)
	floor, err := s.buildFloor(ctx)
	if err != nil {
		if !errors.Is(err, domain.ErrUnauthorized) {
			if stale, ok := s.floor.getFresh(ctx, universe, floorRetain); ok {
				// Serve stale, but never silently — a persistent rebuild
				// failure (not just a rate-limit blip) would otherwise hide
				// behind a frozen floor with no operator signal.
				slog.Warn("floor: serving stale cache after rebuild failed", "err", err)
				return stale, nil
			}
		}
		return domain.Floor{}, err
	}
	s.floor.put(ctx, universe, epoch, floor)
	return floor, nil
}

// buildFloor assembles the universe view's data live: the reader's world list
// (mark_worlds in broker mode, the home world over QUIC), then each world's
// whole-catalog lookup ("*", importance order). A world whose catalog read
// fails still appears, satellite-less and marked (an old server that rejects
// match-all, an unreachable world): on the floor, absence would read as
// nonexistence.
func (s *ReadingService) buildFloor(ctx context.Context) (domain.Floor, error) {
	// The permission mask and host join, shared with the hub read for hubTTL.
	hosts, err := s.readerHosts(ctx)
	if err != nil {
		return domain.Floor{}, err
	}
	worlds := hosts.worlds

	floor := domain.Floor{Worlds: make([]domain.FloorWorld, 0, len(worlds))}
	for _, w := range worlds {
		fw := domain.FloorWorld{World: w}
		// One row past the sample tells a full catalog from a larger one.
		raw, err := s.world.Lookup(ctx, w.Name, domain.LookupQuery{Scope: "/", Query: "*", Limit: floorSample + 1})
		switch {
		case errors.Is(err, domain.ErrUnauthorized):
			// The reader's identity died mid-assembly — that is the
			// page's problem (re-login), not one world's.
			return domain.Floor{}, err
		case err != nil:
			fw.Err = true
		default:
			fw.Docs = parseCatalogTable(raw.Body, floorSample+1)
			if len(fw.Docs) > floorSample {
				fw.Docs, fw.Truncated = fw.Docs[:floorSample], true
			}
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
	hub := s.hubGraph(ctx).topo
	// Concat, not append: hub.edges is the reader's cached topology.
	edges, portals := worldEdges(slices.Concat(hub.edges, s.graph.edges(hosts)), hosts.host2name, hosts.authorized)
	floor.Edges = edges
	for _, p := range portals {
		floor.Worlds = append(floor.Worlds, domain.FloorWorld{
			World: domain.WorldInfo{Name: p, URL: "mark://" + p}, Portal: true,
		})
	}

	return floor, nil
}

// FloorCached serves the reader's last floor within floorRetain, building live
// on a miss. Unfocused floor panes read here — same focused-live policy as
// every other pane.
func (s *ReadingService) FloorCached(ctx context.Context) (domain.Floor, error) {
	if floor, ok := s.floor.getFresh(ctx, universe, floorRetain); ok {
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
type catalogRow struct {
	domain.FloorDoc
	World   string
	Anchor  string   // the matching section, on a body-match row
	Tags    []string // the row's catalog tags, status axis included
	Snippet string   // the matching excerpt, on a body-match row
}

func parseCatalogTable(body string, limit int) []domain.FloorDoc {
	rows := parseCatalogRows(body, "", false, limit)
	if rows == nil {
		return nil
	}
	out := make([]domain.FloorDoc, 0, len(rows))
	for i := range rows {
		out = append(out, rows[i].FloorDoc)
	}
	return out
}

// parseCatalogRows accepts ordinary LOOKUP paths and mark:// URLs emitted by
// mark_lookup_all. fallbackWorld identifies ordinary paths in direct mode.
func parseCatalogRows(body, fallbackWorld string, qualifiedOnly bool, limit int) []catalogRow {
	var out []catalogRow
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := splitCatalogRow(line)
		if len(cells) < 4 {
			continue
		}
		path := unescapeMD(strings.TrimSpace(cells[0]))
		if path == "" || path == "Path" || strings.HasPrefix(path, "-") {
			continue
		}
		world, anchor := fallbackWorld, ""
		if strings.HasPrefix(path, "/") {
			if qualifiedOnly {
				continue
			}
			// Body-match rows name the matching section after the path.
			path, anchor, _ = strings.Cut(path, "#")
		} else {
			u, err := url.Parse(path)
			if err != nil || u.Scheme != "mark" || u.Host == "" || u.User != nil ||
				u.RawQuery != "" || !strings.HasPrefix(u.Path, "/") {
				continue
			}
			world, path, anchor = u.Host, u.Path, u.Fragment
		}
		importance, _ := strconv.ParseFloat(strings.TrimSpace(cells[1]), 64)
		title := unescapeMD(strings.TrimSpace(cells[2]))
		if title == "" {
			title = strings.TrimSuffix(path[strings.LastIndex(path, "/")+1:], ".md")
		}
		tags := splitTags(unescapeMD(strings.TrimSpace(cells[3])))
		row := catalogRow{
			FloorDoc: domain.FloorDoc{
				Path:       path,
				Title:      title,
				Importance: importance,
				Status:     resolveStatus(tags, nil),
			},
			World:  world,
			Anchor: anchor,
			Tags:   tags,
		}
		if len(cells) > 4 {
			row.Snippet = unescapeMD(strings.TrimSpace(cells[4]))
		}
		out = append(out, row)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func splitCatalogRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	var cells []string
	start, backslashes := 0, 0
	for i := range len(line) {
		switch line[i] {
		case '\\':
			backslashes++
		case '|':
			if backslashes%2 == 0 {
				cells = append(cells, line[start:i])
				start = i + 1
			}
			backslashes = 0
		default:
			backslashes = 0
		}
	}
	return append(cells, line[start:])
}

func parseLookupFailureWorlds(body string) []string {
	_, failures, ok := strings.Cut(body, "## World failures")
	if !ok {
		return nil
	}
	var worlds []string
	for line := range strings.SplitSeq(failures, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := splitCatalogRow(line)
		if len(cells) < 2 {
			continue
		}
		world := unescapeMD(strings.TrimSpace(cells[0]))
		if world == "" || world == "World" || strings.HasPrefix(world, "-") {
			continue
		}
		worlds = append(worlds, world)
	}
	return worlds
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
