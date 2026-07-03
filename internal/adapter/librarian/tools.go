package librarian

// The librarian's tools: thin nib agent.Tool wrappers over the core's
// read-only inbound ports (plan D1). Each Execute threads ctx through to the
// port — the asking reader's identity in broker mode — and returns plain text
// shaped for a model, never HTML. Errors come back as IsError tool results so
// the model can recover and say so; malformed arguments never panic.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/latebit-io/demarkus-library/internal/core/port"
	nibagent "github.com/latebit-io/nib/agent"
	"github.com/latebit-io/nib/ai/llm"
)

const (
	// maxOpenBytes caps a document body fed back to the model — token
	// economy over completeness; the truncation note says what was cut.
	maxOpenBytes = 16 * 1024
	// maxFindRows caps find results; past this the model should narrow.
	maxFindRows = 25
)

// errResult wraps err as a tool failure the model can read and react to.
func errResult(err error) nibagent.ToolResult {
	return nibagent.ToolResult{Content: "Error: " + err.Error(), IsError: true}
}

// decodeArgs parses a tool call's raw JSON arguments into dst, tolerating an
// empty argument string (some models send "" for no-arg calls).
func decodeArgs(args string, dst any) error {
	if strings.TrimSpace(args) == "" {
		return nil
	}
	return json.Unmarshal([]byte(args), dst)
}

// worldsTool lists the universe: authorized worlds, portals, and each
// world's top-importance documents (MapService.Floor).
type worldsTool struct{ maps port.MapService }

func (t *worldsTool) Definition() llm.ToolDef {
	return llm.ToolDef{Type: "function", Function: llm.FunctionDef{
		Name:        "worlds",
		Description: "List the universe's worlds and each world's most important documents. Call this first to orient before searching.",
		// Required must be non-nil: strict OpenAI-compatible validators
		// reject "required": null (nib sets []string{} on every tool).
		Parameters: llm.FunctionParams{Type: "object", Properties: map[string]llm.FunctionParam{}, Required: []string{}},
	}}
}

func (t *worldsTool) Execute(ctx context.Context, _ llm.ToolCall) nibagent.ToolResult {
	floor, err := t.maps.Floor(ctx)
	if err != nil {
		return errResult(err)
	}
	var b strings.Builder
	for _, w := range floor.Worlds {
		switch {
		case w.Portal:
			fmt.Fprintf(&b, "portal %s — externally linked, not authorized here\n", w.World.Name)
			continue
		case w.Err:
			fmt.Fprintf(&b, "world %s — catalog unreachable\n", w.World.Name)
			continue
		}
		fmt.Fprintf(&b, "world %s\n", w.World.Name)
		for _, d := range w.Docs {
			fmt.Fprintf(&b, "  %s — %s", d.Path, d.Title)
			if d.Status != "" {
				fmt.Fprintf(&b, " [%s]", d.Status)
			}
			b.WriteByte('\n')
		}
	}
	if b.Len() == 0 {
		return nibagent.ToolResult{Content: "No worlds reachable."}
	}
	return nibagent.ToolResult{Content: b.String()}
}

// findTool locates documents by title/path substring over the catalog's
// name index (Reader.NameIndex). Name match only — the protocol has no
// full-text search, and the tool says so rather than pretending.
type findTool struct{ reader port.Reader }

func (t *findTool) Definition() llm.ToolDef {
	return llm.ToolDef{Type: "function", Function: llm.FunctionDef{
		Name:        "find",
		Description: "Find documents whose title or path contains the query (case-insensitive name match — NOT full-text content search). Omit world to search every authorized world.",
		Parameters: llm.FunctionParams{Type: "object", Properties: map[string]llm.FunctionParam{
			"query": {Type: "string", Description: "substring to match against document titles and paths"},
			"world": {Type: "string", Description: "restrict to one world (default: all worlds)"},
		}, Required: []string{"query"}},
	}}
}

func (t *findTool) Execute(ctx context.Context, call llm.ToolCall) nibagent.ToolResult {
	var in struct{ Query, World string }
	if err := decodeArgs(call.Function.Arguments, &in); err != nil {
		return errResult(err)
	}
	if strings.TrimSpace(in.Query) == "" {
		return errResult(fmt.Errorf("find: query is required"))
	}
	scope := "universe"
	if in.World != "" {
		scope = "world"
	}
	entries, err := t.reader.NameIndex(ctx, scope, in.World)
	if err != nil {
		return errResult(err)
	}
	q := strings.ToLower(in.Query)
	var b strings.Builder
	matches := 0
	for _, e := range entries {
		if !strings.Contains(strings.ToLower(e.Title), q) && !strings.Contains(strings.ToLower(e.Path), q) {
			continue
		}
		matches++
		if matches > maxFindRows {
			fmt.Fprintf(&b, "… more matches — narrow the query\n")
			break
		}
		fmt.Fprintf(&b, "mark://%s%s — %s", e.World, e.Path, e.Title)
		if e.Status != "" {
			fmt.Fprintf(&b, " [%s]", e.Status)
		}
		b.WriteByte('\n')
	}
	if matches == 0 {
		return nibagent.ToolResult{Content: fmt.Sprintf("No titles or paths match %q.", in.Query)}
	}
	return nibagent.ToolResult{Content: b.String()}
}

// openTool reads one document's raw markdown and catalog metadata
// (Reader.Raw) — the librarian's primary grounding move.
type openTool struct {
	reader       port.Reader
	defaultWorld string
}

func (t *openTool) Definition() llm.ToolDef {
	return llm.ToolDef{Type: "function", Function: llm.FunctionDef{
		Name:        "open",
		Description: "Read a document's raw markdown source and catalog metadata. path is the document path (e.g. /plans/roadmap.md); paths ending in / are directory listings and cannot be opened — find their documents instead.",
		Parameters: llm.FunctionParams{Type: "object", Properties: map[string]llm.FunctionParam{
			"path":  {Type: "string", Description: "document path within the world"},
			"world": {Type: "string", Description: "world to read from (default: the reading room's default world)"},
		}, Required: []string{"path"}},
	}}
}

func (t *openTool) Execute(ctx context.Context, call llm.ToolCall) nibagent.ToolResult {
	var in struct{ Path, World string }
	if err := decodeArgs(call.Function.Arguments, &in); err != nil {
		return errResult(err)
	}
	if strings.TrimSpace(in.Path) == "" {
		return errResult(fmt.Errorf("open: path is required"))
	}
	world := in.World
	if world == "" {
		world = t.defaultWorld
	}
	raw, err := t.reader.Raw(ctx, world, in.Path)
	if err != nil {
		return errResult(err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "mark://%s%s\n", world, in.Path)
	keys := make([]string, 0, len(raw.Metadata))
	for k := range raw.Metadata {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s: %s\n", k, raw.Metadata[k])
	}
	b.WriteString("\n")
	body := raw.Body
	if len(body) > maxOpenBytes {
		// Back off to a rune boundary so the cut never splits a UTF-8
		// sequence — an invalid tail would corrupt the model's context.
		cut := maxOpenBytes
		for cut > 0 && !utf8.RuneStart(body[cut]) {
			cut--
		}
		body = body[:cut] + fmt.Sprintf("\n\n[truncated — %d more bytes]", len(raw.Body)-cut)
	}
	b.WriteString(body)
	return nibagent.ToolResult{Content: b.String()}
}

// linksTool traces a document's neighborhood: observed outbound links and
// backlinks (GraphService.Neighborhood). Store-only — zero world reads.
type linksTool struct {
	graph        port.GraphService
	defaultWorld string
}

func (t *linksTool) Definition() llm.ToolDef {
	return llm.ToolDef{Type: "function", Function: llm.FunctionDef{
		Name:        "links",
		Description: "Show the documents a document links to and the documents observed linking to it (backlinks). The graph fills as the room is read — an empty answer means no edges observed yet, not no edges.",
		Parameters: llm.FunctionParams{Type: "object", Properties: map[string]llm.FunctionParam{
			"path":  {Type: "string", Description: "document path within the world"},
			"world": {Type: "string", Description: "world the document lives in (default: the reading room's default world)"},
		}, Required: []string{"path"}},
	}}
}

func (t *linksTool) Execute(_ context.Context, call llm.ToolCall) nibagent.ToolResult {
	var in struct{ Path, World string }
	if err := decodeArgs(call.Function.Arguments, &in); err != nil {
		return errResult(err)
	}
	if strings.TrimSpace(in.Path) == "" {
		return errResult(fmt.Errorf("links: path is required"))
	}
	world := in.World
	if world == "" {
		world = t.defaultWorld
	}
	n := t.graph.Neighborhood(world, in.Path)
	var b strings.Builder
	fmt.Fprintf(&b, "mark://%s%s\n", n.Center.World, n.Center.Path)
	if len(n.Out) == 0 && len(n.In) == 0 {
		b.WriteString("No edges observed yet.\n")
		return nibagent.ToolResult{Content: b.String()}
	}
	if len(n.Out) > 0 {
		b.WriteString("links to:\n")
		for _, r := range n.Out {
			fmt.Fprintf(&b, "  mark://%s%s\n", r.World, r.Path)
		}
	}
	if len(n.In) > 0 {
		b.WriteString("referenced by:\n")
		for _, r := range n.In {
			fmt.Fprintf(&b, "  mark://%s%s\n", r.World, r.Path)
		}
	}
	return nibagent.ToolResult{Content: b.String()}
}

// compactArgs renders a tool call's JSON arguments as `key="value"` pairs in
// sorted key order for the one-line trace; unparseable args pass through raw
// (truncated) rather than hiding what the model attempted.
func compactArgs(args string) string {
	trimmed := strings.TrimSpace(args)
	if trimmed == "" || trimmed == "{}" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(trimmed), &m); err != nil {
		if len(trimmed) > 80 {
			trimmed = trimmed[:80] + "…"
		}
		return trimmed
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, quoteIfString(m[k])))
	}
	return strings.Join(parts, " ")
}

// quoteIfString quotes string values in trace lines so empty and spacey
// values stay legible; other JSON scalars render bare.
func quoteIfString(v any) string {
	if s, ok := v.(string); ok {
		return fmt.Sprintf("%q", s)
	}
	return fmt.Sprint(v)
}
