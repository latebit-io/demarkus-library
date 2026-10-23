package librarian

import (
	"context"
	"fmt"
	"strings"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus-library/internal/core/port"
	nibagent "github.com/latebit-io/nib/agent"
	"github.com/latebit-io/nib/ai/llm"
)

const (
	// mapClusterDocs is how many leading documents a cluster shows the model;
	// the rest are a count, the way the world map draws a "+N more" bubble.
	mapClusterDocs = 6
	// mapMaxEdges bounds the link list; a dense world would otherwise flood a turn.
	mapMaxEdges = 40
)

// mapTool describes a world's shape (MapService.WorldMapCached): its
// directory clusters with their leading documents, and the links observed
// among the mapped documents.
type mapTool struct {
	maps         port.MapService
	defaultWorld string
}

// mapArgs are the map tool's arguments.
type mapArgs struct {
	World string `json:"world"`
}

func (t *mapTool) Definition() llm.ToolDef {
	return llm.ToolDef{Type: "function", Function: llm.FunctionDef{
		Name:        "map",
		Description: "Map a world: its directory clusters with their leading documents, and the links among the mapped documents. Use it to see how a world hangs together; worlds lists the worlds to map, links traces one document's neighbors.",
		Parameters: llm.FunctionParams{Type: "object", Properties: map[string]llm.FunctionParam{
			"world": {Type: "string", Description: "world to map (default: the reading room's default world)"},
		}, Required: []string{}},
	}}
}

// world decodes the call's world, falling back to the room's default.
func (t *mapTool) world(args string) (string, error) {
	var in mapArgs
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	if w := strings.TrimSpace(in.World); w != "" {
		return w, nil
	}
	return t.defaultWorld, nil
}

func (t *mapTool) step(args string) domain.LibrarianStep {
	world, err := t.world(args)
	if err != nil {
		return domain.LibrarianStep{}
	}
	return domain.LibrarianStep{Text: "mapped world " + world}
}

func (t *mapTool) Execute(ctx context.Context, call llm.ToolCall) nibagent.ToolResult {
	world, err := t.world(call.Function.Arguments)
	if err != nil {
		return errResult(err)
	}
	wm, err := t.maps.WorldMapCached(ctx, world)
	if err != nil {
		return errResult(err)
	}
	if wm.Unreadable {
		return errResult(fmt.Errorf("map: the catalog of world %s is unreadable", world))
	}
	total := 0
	for _, cl := range wm.Clusters {
		total += len(cl.Docs) + cl.More
	}
	if total == 0 {
		return nibagent.ToolResult{Content: fmt.Sprintf("World %s holds no documents.\n", world)}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "world %s — %d documents in %d clusters\n", world, total, len(wm.Clusters))
	for _, cl := range wm.Clusters {
		name := cl.Dir + "/"
		if cl.Dir == "" {
			name = "(root)"
		}
		fmt.Fprintf(&b, "%s (%d):\n", name, len(cl.Docs)+cl.More)
		shown := cl.Docs[:min(len(cl.Docs), mapClusterDocs)]
		writeFloorDocs(&b, shown)
		if rest := len(cl.Docs) - len(shown) + cl.More; rest > 0 {
			fmt.Fprintf(&b, "  +%d more under %s\n", rest, cl.ListPath)
		}
	}
	if len(wm.Edges) > 0 {
		b.WriteString("links among the mapped documents:\n")
		for _, e := range wm.Edges[:min(len(wm.Edges), mapMaxEdges)] {
			fmt.Fprintf(&b, "  %s -> %s\n", e.From.Path, e.To.Path)
		}
		if rest := len(wm.Edges) - mapMaxEdges; rest > 0 {
			fmt.Fprintf(&b, "  +%d more links\n", rest)
		}
	}
	return nibagent.ToolResult{Content: b.String()}
}
