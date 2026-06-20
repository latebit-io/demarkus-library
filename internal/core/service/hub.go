package service

import (
	"context"
	"sort"
	"strings"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// The hub topology reader (plans "Floor enrichment", decision 11). A hub is
// just a demarkus world holding the published universe map; the federation
// agent (brokered) or an operator (single-world) publishes a graph export
// there at hubGraphPath. The export is a strict superset of the hash index —
// nodes (with title/status) AND edges in one document — so it is the floor's
// single durable, versioned topology source. The library only READS it; it is
// never a writer (decision 11 — projection never source).
//
// Everything degrades: no hub configured, hub unreachable, or no graph doc
// published yet ⇒ readHub returns the zero value and the floor falls back to
// mark_worlds + per-world lookup with edges from the R3 observed-links map.
const hubGraphPath = "/graph.md"

// hubNode is one node from the graph export's Nodes table.
type hubNode struct {
	Ref    domain.Ref
	Status string
}

// hubTopology is the parsed graph export: the universe's nodes and edges, keyed
// by host (the export addresses everything as mark://host/path).
type hubTopology struct {
	nodes []hubNode
	edges []domain.Edge
}

// readHub fetches and parses the hub's published graph export. hub is the world
// id of the topology source (DEMARKUS_HUB; the home world in quic). A missing
// doc or any read/parse failure yields an empty topology — the floor degrades,
// never errors, on the enrichment layer.
func (s *ReadingService) readHub(ctx context.Context, hub string) hubTopology {
	if hub == "" {
		return hubTopology{}
	}
	raw, err := s.world.Fetch(ctx, hub, hubGraphPath)
	if err != nil {
		return hubTopology{}
	}
	return parseGraphExport(raw.Body)
}

// parseGraphExport reads the mark_graph_export document format: a "## Nodes"
// table (| URL | Title | Status | Links |) and a "## Edges" table
// (| From | To |), both with mark:// URLs. Rows that are not mark:// nodes
// (external https links, header/separator rows) are skipped. The two tables
// are told apart by their column count, so a light shape-parse is honest
// regardless of section order.
func parseGraphExport(body string) hubTopology {
	var t hubTopology
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		switch {
		case len(cells) >= 4: // Nodes: | URL | Title | Status | Links |
			ref, ok := parseMarkRef(unlinkMD(cells[0]))
			if !ok {
				continue
			}
			t.nodes = append(t.nodes, hubNode{Ref: ref, Status: cells[2]})
		case len(cells) == 2: // Edges: | From | To |
			from, okF := parseMarkRef(cells[0])
			to, okT := parseMarkRef(cells[1])
			if okF && okT {
				t.edges = append(t.edges, domain.Edge{From: from, To: to, Type: domain.EdgeReference})
			}
		}
	}
	return t
}

// parseMarkRef turns a "mark://host[:port]/path" string into a Ref (World =
// host, Path = "/..."). Non-mark URLs (external https, the table header) return
// ok=false. A bare host or trailing slash normalizes to path "/".
func parseMarkRef(s string) (domain.Ref, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(s), "mark://")
	if !ok || rest == "" {
		return domain.Ref{}, false
	}
	host, path, found := strings.Cut(rest, "/")
	if host == "" {
		return domain.Ref{}, false
	}
	if !found || path == "" {
		path = "/"
	} else {
		path = "/" + path
	}
	return domain.Ref{World: strings.ToLower(host), Path: path}, true
}

// unlinkMD unwraps a markdown link "[text](url)" to its url, leaving plain
// text untouched — the export's Nodes table wraps each URL as a link.
func unlinkMD(s string) string {
	if !strings.HasPrefix(s, "[") {
		return s
	}
	if i := strings.Index(s, "]("); i >= 0 {
		if j := strings.LastIndex(s, ")"); j > i+1 {
			return s[i+2 : j]
		}
	}
	return s
}

// hostName resolves a topology Ref's world to an authorized world name. The
// graph export keys nodes by host; mark_worlds gives each authorized world's
// host (its URL), so host→name joins the two. A ref whose world is already an
// authorized name (the observed-links map uses the library's own world ids)
// passes through. Anything unmatched is an external host → a portal.
func hostName(world string, host2name map[string]string, authorized map[string]bool) (string, bool) {
	if authorized[world] {
		return world, false // already an authorized name
	}
	if name, ok := host2name[strings.ToLower(world)]; ok {
		return name, false
	}
	return world, true // unmatched host → portal
}

// hostOf extracts the host from a world's mark:// URL, "" when absent.
func hostOf(url string) string {
	rest, ok := strings.CutPrefix(strings.TrimSpace(url), "mark://")
	if !ok {
		return ""
	}
	host, _, _ := strings.Cut(rest, "/")
	return strings.ToLower(host)
}

// worldEdges aggregates document-level edges (hub graph ∪ observed map) into
// the world-level connections the floor draws: a deduped set of From-world →
// To-world pairs, skipping intra-world links (not meaningful at universe
// scale). It also returns the portal worlds — edge endpoints with no
// authorized name, i.e. externally-linked hosts (the extensional universe,
// ADR 0005 §16). Both outputs are sorted for a stable, cacheable render.
func worldEdges(edges []domain.Edge, host2name map[string]string, authorized map[string]bool) (worldLevel []domain.Edge, portalNames []string) {
	seen := map[domain.Edge]struct{}{}
	portals := map[string]bool{}
	var out []domain.Edge
	for _, e := range edges {
		from, fp := hostName(e.From.World, host2name, authorized)
		to, tp := hostName(e.To.World, host2name, authorized)
		if from == to {
			continue
		}
		if fp {
			portals[from] = true
		}
		if tp {
			portals[to] = true
		}
		we := domain.Edge{From: domain.Ref{World: from}, To: domain.Ref{World: to}, Type: e.Type}
		if _, dup := seen[we]; dup {
			continue
		}
		seen[we] = struct{}{}
		out = append(out, we)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].From.World != out[j].From.World {
			return out[i].From.World < out[j].From.World
		}
		return out[i].To.World < out[j].To.World
	})
	names := make([]string, 0, len(portals))
	for p := range portals {
		names = append(names, p)
	}
	sort.Strings(names)
	return out, names
}
