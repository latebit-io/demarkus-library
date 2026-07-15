// Package graphexport adapts the demarkus client's graph-export parser to
// the core's GraphExportParser port, lifting raw export rows into domain
// graph terms.
package graphexport

import (
	"strings"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus/client/graphstore"
)

// Parser implements port.GraphExportParser via graphstore.ParseExport, the
// contract-tested parser for both the legacy two-column and the enriched
// six-column edge tables.
type Parser struct{}

// ParseGraphExport decodes body into domain nodes and reference edges:
// mark:// endpoints only, and one edge per document pair. Enriched exports
// repeat a pair as a plain plus rel-typed rows; the pair keeps its first
// declared predicate so the relation survives the collapse.
func (Parser) ParseGraphExport(body string) ([]domain.GraphNode, []domain.Edge) {
	rawNodes, rawEdges := graphstore.ParseExport(body)
	var nodes []domain.GraphNode
	for i := range rawNodes {
		ref, ok := parseMarkRef(rawNodes[i].URL)
		if !ok {
			continue
		}
		nodes = append(nodes, domain.GraphNode{Ref: ref, Status: rawNodes[i].Status})
	}
	var edges []domain.Edge
	type pair struct{ from, to domain.Ref }
	at := map[pair]int{}
	for i := range rawEdges {
		from, okF := parseMarkRef(rawEdges[i].From)
		to, okT := parseMarkRef(rawEdges[i].To)
		if !okF || !okT {
			continue
		}
		p := pair{from, to}
		if j, dup := at[p]; dup {
			if edges[j].Rel == "" {
				edges[j].Rel = rawEdges[i].Rel
			}
			continue
		}
		at[p] = len(edges)
		edges = append(edges, domain.Edge{From: from, To: to, Type: domain.EdgeReference, Rel: rawEdges[i].Rel})
	}
	return nodes, edges
}

// parseMarkRef turns a "mark://host[:port]/path" string into a Ref (World =
// host, Path = "/..."). Non-mark URLs (external https links) return
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
