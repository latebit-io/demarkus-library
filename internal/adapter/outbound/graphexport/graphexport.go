// Package graphexport adapts the demarkus client's graph-export parser to
// the core's GraphExportParser port.
package graphexport

import (
	"github.com/latebit-io/demarkus-library/internal/core/port"
	"github.com/latebit-io/demarkus/client/graphstore"
)

// Parser implements port.GraphExportParser via graphstore.ParseExport, the
// contract-tested parser for both the legacy two-column and the enriched
// six-column edge tables.
type Parser struct{}

// ParseGraphExport decodes body into raw export rows.
func (Parser) ParseGraphExport(body string) ([]port.GraphExportNode, []port.GraphExportEdge) {
	nodes, edges := graphstore.ParseExport(body)
	outNodes := make([]port.GraphExportNode, 0, len(nodes))
	for i := range nodes {
		outNodes = append(outNodes, port.GraphExportNode{URL: nodes[i].URL, Status: nodes[i].Status})
	}
	outEdges := make([]port.GraphExportEdge, 0, len(edges))
	for i := range edges {
		outEdges = append(outEdges, port.GraphExportEdge{From: edges[i].From, To: edges[i].To})
	}
	return outNodes, outEdges
}
