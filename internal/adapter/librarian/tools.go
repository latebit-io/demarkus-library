package librarian

// The librarian's tools: thin nib agent.Tool wrappers over the core's
// read-only inbound ports (plan D1). Each Execute threads ctx through to the
// port — the asking reader's identity in broker mode — and returns plain text
// shaped for a model, never HTML. Errors come back as IsError tool results so
// the model can recover and say so; malformed arguments never panic.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus-library/internal/core/port"
	"github.com/latebit-io/demarkus/client/mdoutline"
	nibagent "github.com/latebit-io/nib/agent"
	"github.com/latebit-io/nib/ai/llm"
)

const (
	// maxOpenBytes caps a document body fed back to the model — token
	// economy over completeness; the truncation note says what was cut.
	maxOpenBytes = 16 * 1024
	// outlineThreshold is the body size above which open returns an
	// outline (heading tree + opening paragraph) instead of the body.
	// Matches the demarkus MCP surfaces' mark_fetch threshold, and the
	// anchors are the same GitHub-style slugs — path#anchor means the
	// same section here, in mark_fetch, and in an MCP resource URI.
	outlineThreshold = 8 * 1024
	// maxFindRows caps find results; past this the model should narrow.
	maxFindRows = 25
)

// stepDescriber narrates one call of a tool as a trace line a reader can
// follow: what the librarian did, and the document it touched.
type stepDescriber interface {
	step(args string) domain.LibrarianStep
}

// toolWithStep is a librarian tool: callable by the model, legible in the
// trace.
type toolWithStep interface {
	nibagent.Tool
	stepDescriber
}

// describe narrates a tool call; an unknown tool or malformed arguments fall
// back to the raw call, so the trace never hides what the model attempted.
func (l *Librarian) describe(name, args string) domain.LibrarianStep {
	if d, ok := l.steps[name]; ok {
		if step := d.step(args); step.Text != "" {
			return step
		}
	}
	return domain.LibrarianStep{Text: traceLine(name, args)}
}

// docArgs are the arguments every document-addressed tool takes.
type docArgs struct{ Path, World string }

// docRef decodes a document-addressed call into the document it touches,
// anchor stripped; ok is false when the path is missing or malformed.
func docRef(args, defaultWorld string) (domain.Ref, string, bool) {
	var in docArgs
	if decodeArgs(args, &in) != nil || strings.TrimSpace(in.Path) == "" {
		return domain.Ref{}, "", false
	}
	world := in.World
	if world == "" {
		world = defaultWorld
	}
	docPath, anchor, _ := strings.Cut(in.Path, "#")
	return domain.Ref{World: world, Path: docPath}, anchor, true
}

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

func (t *worldsTool) step(string) domain.LibrarianStep {
	return domain.LibrarianStep{Text: "surveyed the worlds"}
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

func (t *findTool) step(args string) domain.LibrarianStep {
	var in struct{ Query, World string }
	if decodeArgs(args, &in) != nil || in.Query == "" {
		return domain.LibrarianStep{}
	}
	return domain.LibrarianStep{Text: fmt.Sprintf("searched names for %q%s", in.Query, inWorld(in.World))}
}

// inWorld phrases an optional world scope for a trace line.
func inWorld(world string) string {
	if world == "" {
		return ""
	}
	return " in " + world
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
	var partial *domain.PartialLookupError
	if err != nil && !errors.As(err, &partial) {
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
		if partial != nil {
			return nibagent.ToolResult{Content: partial.Error() + ". No matches in available worlds."}
		}
		return nibagent.ToolResult{Content: fmt.Sprintf("No titles or paths match %q.", in.Query)}
	}
	if partial != nil {
		fmt.Fprintf(&b, "%s.\n", partial.Error())
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
		Description: "Read a document's raw markdown source and catalog metadata. path is the document path (e.g. /plans/roadmap.md); append #<anchor> to read one section (anchors are GitHub-style heading slugs). Documents over 8KB return an outline — heading tree with anchors and the opening paragraph — instead of the body; open path#<anchor> for the section you need, or pass force for the full (16KB-capped) body. path/v<N> reads edition N (see versions). Paths ending in / are directory listings and cannot be opened — find their documents instead.",
		Parameters: llm.FunctionParams{Type: "object", Properties: map[string]llm.FunctionParam{
			"path":  {Type: "string", Description: "document path within the world; append #<anchor> for a single section"},
			"world": {Type: "string", Description: "world to read from (default: the reading room's default world)"},
			"force": {Type: "boolean", Description: "return the full body even when the document is large (still capped at 16KB)"},
		}, Required: []string{"path"}},
	}}
}

func (t *openTool) step(args string) domain.LibrarianStep {
	ref, anchor, ok := docRef(args, t.defaultWorld)
	if !ok {
		return domain.LibrarianStep{}
	}
	if anchor != "" {
		return domain.LibrarianStep{Text: "opened §" + anchor + " of", Ref: ref}
	}
	return domain.LibrarianStep{Text: "opened", Ref: ref}
}

func (t *openTool) Execute(ctx context.Context, call llm.ToolCall) nibagent.ToolResult {
	var in struct{ Force bool }
	if err := decodeArgs(call.Function.Arguments, &in); err != nil {
		return errResult(err)
	}
	ref, anchor, ok := docRef(call.Function.Arguments, t.defaultWorld)
	if !ok {
		return errResult(errors.New("open: path is required"))
	}
	world, docPath := ref.World, ref.Path
	raw, err := t.reader.Raw(ctx, world, docPath)
	if err != nil {
		return errResult(err)
	}
	if r := runFrom(ctx); r != nil {
		r.addSource(domain.LibrarianSource{Ref: ref, Title: sourceTitle(raw.Metadata["title"], docPath)})
	}

	var b strings.Builder
	fmt.Fprintf(&b, "mark://%s%s\n", world, docPath)
	keys := make([]string, 0, len(raw.Metadata))
	for k := range raw.Metadata {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s: %s\n", k, raw.Metadata[k])
	}

	body := raw.Body
	switch {
	case anchor != "":
		// Section open: works at any size, same anchors as the demarkus
		// MCP surfaces (mdoutline is the shared implementation).
		section, ok := mdoutline.Section(body, anchor)
		if !ok {
			available := strings.Join(mdoutline.Anchors(body), ", ")
			if available == "" {
				available = "(document has no headings)"
			}
			return errResult(fmt.Errorf("open: section #%s not found in mark://%s%s; available anchors: %s", anchor, world, docPath, available))
		}
		fmt.Fprintf(&b, "section: #%s\n", anchor)
		body = section
	case !in.Force && len(body) >= outlineThreshold:
		// Outline mode replaces the old blind truncation: the model gets
		// the whole document's map and a way to reach every section,
		// instead of losing the tail past 16KB with no recourse.
		fmt.Fprintf(&b, "mode: outline\nsize: %d bytes, %d lines\n", len(body), strings.Count(body, "\n")+1)
		var o strings.Builder
		if tree := mdoutline.Outline(body); tree != "" {
			o.WriteString(tree)
		} else {
			o.WriteString("(document has no headings)\n")
		}
		if para := mdoutline.OpeningParagraph(body); para != "" {
			o.WriteString("\n")
			o.WriteString(para)
			o.WriteString("\n")
		}
		// Outline mode owns its truncation so a pathologically
		// heading-heavy outline can never lose the navigation footer to
		// the generic cut below — that would recreate the dead end this
		// mode exists to remove.
		footer := fmt.Sprintf("\nopen %s#<anchor> for a section; force for the full body\n", docPath)
		outline := o.String()
		if cut, dropped := truncateRuneSafe(outline, maxOpenBytes-len(footer)-64); dropped > 0 {
			outline = cut + fmt.Sprintf("\n\n[outline truncated — %d more bytes]", dropped)
		}
		body = outline + footer
	}

	b.WriteString("\n")
	if cut, dropped := truncateRuneSafe(body, maxOpenBytes); dropped > 0 {
		body = cut + fmt.Sprintf("\n\n[truncated — %d more bytes]", dropped)
	}
	b.WriteString(body)
	return nibagent.ToolResult{Content: b.String()}
}

// truncateRuneSafe cuts s to at most limit bytes, backing off to a rune
// boundary so the cut never splits a UTF-8 sequence — an invalid tail
// would corrupt the model's context. Returns the cut string and how many
// bytes were dropped (0 when s already fits).
func truncateRuneSafe(s string, limit int) (cutStr string, dropped int) {
	if len(s) <= limit {
		return s, 0
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], len(s) - cut
}

// sourceTitle names an opened document for the sources list: its catalog
// title, else its file name.
func sourceTitle(title, docPath string) string {
	if title = strings.TrimSpace(title); title != "" {
		return title
	}
	return strings.TrimSuffix(path.Base(docPath), ".md")
}

// traceLine renders one tool call raw, as a single legible line:
// `find {"query":"deploy"}` → `find query="deploy"`.
func traceLine(name, args string) string {
	fields := compactArgs(args)
	if fields == "" {
		return name
	}
	return name + " " + fields
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
