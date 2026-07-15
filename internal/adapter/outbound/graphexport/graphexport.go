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
// mark:// endpoints only, and one edge per document pair (enriched exports
// repeat a pair with rel-typed rows).
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
	seen := map[domain.Edge]struct{}{}
	for i := range rawEdges {
		from, okF := parseMarkRef(rawEdges[i].From)
		to, okT := parseMarkRef(rawEdges[i].To)
		if !okF || !okT {
			continue
		}
		e := domain.Edge{From: from, To: to, Type: domain.EdgeReference}
		if _, dup := seen[e]; dup {
			continue
		}
		seen[e] = struct{}{}
		edges = append(edges, e)
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
