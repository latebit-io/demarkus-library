package graphexport

import (
	"reflect"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// A trimmed legacy (two-column edge) export with an external row and an
// https edge endpoint that must both drop out.
const legacyExport = `# Document Graph

## Nodes

| URL | Title | Status | Links |
|-----|-------|--------|-------|
| [https://github.com/x](https://github.com/x) |  | external | 0 |
| [mark://root.svc:6309/index.md](mark://root.svc:6309/index.md) | Root | ok | 2 |
| [mark://world-a.svc:6309/guide.md](mark://world-a.svc:6309/guide.md) | Guide | ok | 1 |

## Edges

| From | To |
|------|----|
| mark://root.svc:6309/index.md | mark://world-a.svc:6309/guide.md |
| mark://world-a.svc:6309/guide.md | mark://wiki.example.org/notes.md |
| https://github.com/x | mark://root.svc:6309/index.md |
`

// An enriched (six-column edge) export, the shape agent 0.21.1+ publishes;
// the same doc pair appears as a plain and a rel-typed row and must collapse.
const enrichedExport = `# Document Graph

> Exported: GOLDEN

## Nodes

| URL | Title | Status | Links |
|-----|-------|--------|-------|
| [mark://root.svc:6309/index.md](mark://root.svc:6309/index.md) | Root | ok | 2 |
| [mark://world-a.svc:6309/guide.md](mark://world-a.svc:6309/guide.md) | Guide | ok | 1 |

## Edges

| From | To | Rel | Label | Anchor | Count |
|------|----|-----|-------|--------|-------|
| mark://root.svc:6309/index.md | mark://world-a.svc:6309/guide.md |  | Guide | services | 1 |
| mark://root.svc:6309/index.md | mark://world-a.svc:6309/guide.md | supersedes |  |  | 1 |
| mark://world-a.svc:6309/guide.md | mark://wiki.example.org/notes.md |  | notes |  | 2 |
`

var wantNodes = []domain.GraphNode{
	{Ref: domain.Ref{World: "root.svc:6309", Path: "/index.md"}, Status: "ok"},
	{Ref: domain.Ref{World: "world-a.svc:6309", Path: "/guide.md"}, Status: "ok"},
}

var wantEdges = []domain.Edge{
	{From: domain.Ref{World: "root.svc:6309", Path: "/index.md"}, To: domain.Ref{World: "world-a.svc:6309", Path: "/guide.md"}, Type: domain.EdgeReference},
	{From: domain.Ref{World: "world-a.svc:6309", Path: "/guide.md"}, To: domain.Ref{World: "wiki.example.org", Path: "/notes.md"}, Type: domain.EdgeReference},
}

// The enriched fixture's plain and rel-typed rows for the same pair collapse
// to one edge that keeps the predicate.
var wantEnrichedEdges = []domain.Edge{
	{From: domain.Ref{World: "root.svc:6309", Path: "/index.md"}, To: domain.Ref{World: "world-a.svc:6309", Path: "/guide.md"}, Type: domain.EdgeReference, Rel: "supersedes"},
	{From: domain.Ref{World: "world-a.svc:6309", Path: "/guide.md"}, To: domain.Ref{World: "wiki.example.org", Path: "/notes.md"}, Type: domain.EdgeReference},
}

func TestParseGraphExportLegacy(t *testing.T) {
	nodes, edges := Parser{}.ParseGraphExport(legacyExport)
	if !reflect.DeepEqual(nodes, wantNodes) {
		t.Errorf("nodes = %+v, want %+v (external skipped)", nodes, wantNodes)
	}
	if !reflect.DeepEqual(edges, wantEdges) {
		t.Errorf("edges = %+v, want %+v (https endpoint dropped)", edges, wantEdges)
	}
}

// TestParseGraphExportEnriched pins the six-column format: edge rows must
// never become nodes, and the rel-typed duplicate pair collapses to one edge
// (the library's pre-adapter parser classified tables by column count and
// got both wrong).
func TestParseGraphExportEnriched(t *testing.T) {
	nodes, edges := Parser{}.ParseGraphExport(enrichedExport)
	if !reflect.DeepEqual(nodes, wantNodes) {
		t.Errorf("nodes = %+v, want %+v (edge rows must not become nodes)", nodes, wantNodes)
	}
	if !reflect.DeepEqual(edges, wantEnrichedEdges) {
		t.Errorf("edges = %+v, want %+v (pair collapsed, predicate kept)", edges, wantEnrichedEdges)
	}
}

func TestParseMarkRef(t *testing.T) {
	cases := map[string]domain.Ref{
		"mark://h:6309/a/b.md": {World: "h:6309", Path: "/a/b.md"},
		"mark://h:6309/":       {World: "h:6309", Path: "/"},
		"mark://h:6309":        {World: "h:6309", Path: "/"},
		"mark://Host/X.md":     {World: "host", Path: "/X.md"}, // host lowercased, path kept
	}
	for in, want := range cases {
		if got, ok := parseMarkRef(in); !ok || got != want {
			t.Errorf("parseMarkRef(%q) = %+v ok=%v, want %+v", in, got, ok, want)
		}
	}
	for _, bad := range []string{"https://x.com/a", "", "mark://", "  | From "} {
		if _, ok := parseMarkRef(bad); ok {
			t.Errorf("parseMarkRef(%q) accepted", bad)
		}
	}
}
