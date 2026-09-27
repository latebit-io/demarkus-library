package librarian

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus-library/internal/core/port"
	nibagent "github.com/latebit-io/nib/agent"
	"github.com/latebit-io/nib/ai/llm"
)

// maxLookupHits caps lookup results; past this the model should narrow.
const maxLookupHits = 15

// lookupTool is the catalog search (Catalog.Lookup): subjects matched against
// tags and titles, ranked by importance, or section text when full_text asks.
type lookupTool struct{ catalog port.Catalog }

// lookupArgs are the lookup tool's arguments.
type lookupArgs struct {
	Query    string `json:"query"`
	Tag      string `json:"tag"`
	World    string `json:"world"`
	FullText bool   `json:"full_text"`
}

func (t *lookupTool) Definition() llm.ToolDef {
	return llm.ToolDef{Type: "function", Function: llm.FunctionDef{
		Name:        "lookup",
		Description: "Search the catalog by subject: documents whose tags or titles match the query, most important first. Set full_text to search section text instead; hits then name the matching section as path#anchor with a snippet. tag narrows to documents carrying that tag; world narrows to one world (default: every authorized world). Give a query, a tag, or both.",
		Parameters: llm.FunctionParams{Type: "object", Properties: map[string]llm.FunctionParam{
			"query":     {Type: "string", Description: "subject to look up (at least 2 characters)"},
			"tag":       {Type: "string", Description: "only documents carrying this tag"},
			"world":     {Type: "string", Description: "restrict to one world (default: all worlds)"},
			"full_text": {Type: "boolean", Description: "search section text rather than tags and titles"},
		}, Required: []string{}},
	}}
}

func (t *lookupTool) step(args string) domain.LibrarianStep {
	var in lookupArgs
	if decodeArgs(args, &in) != nil || (in.Query == "" && in.Tag == "") {
		return domain.LibrarianStep{}
	}
	text := "looked up"
	if in.Query != "" {
		text += fmt.Sprintf(" %q", in.Query)
	}
	if in.Tag != "" {
		text += " tagged #" + in.Tag
	}
	if in.FullText {
		text += " in section text"
	}
	return domain.LibrarianStep{Text: text + inWorld(in.World)}
}

func (t *lookupTool) Execute(ctx context.Context, call llm.ToolCall) nibagent.ToolResult {
	var in lookupArgs
	if err := decodeArgs(call.Function.Arguments, &in); err != nil {
		return errResult(err)
	}
	in.Query, in.Tag = strings.TrimSpace(in.Query), strings.TrimPrefix(strings.TrimSpace(in.Tag), "#")
	if in.Query == "" && in.Tag == "" {
		return errResult(errors.New("lookup: give a query or a tag"))
	}
	q := domain.CatalogQuery{World: in.World, Query: in.Query, Tag: in.Tag, Limit: maxLookupHits}
	if in.FullText {
		q.Match = domain.MatchBody
	}
	result, err := t.catalog.Lookup(ctx, q)
	var partial *domain.PartialLookupError
	if err != nil && !errors.As(err, &partial) {
		return errResult(err)
	}

	var b strings.Builder
	if in.FullText && !result.BodyMatched {
		b.WriteString("Section-text search is not available here; these are tag and title matches. Do not retry full_text in this world.\n")
	}
	for i := range result.Hits {
		hit := &result.Hits[i]
		fmt.Fprintf(&b, "mark://%s%s", hit.World, hit.Path)
		if hit.Anchor != "" {
			b.WriteString("#" + hit.Anchor)
		}
		fmt.Fprintf(&b, " — %s", hit.Title)
		if hit.Status != "" {
			fmt.Fprintf(&b, " [%s]", hit.Status)
		}
		if len(hit.Tags) > 0 {
			fmt.Fprintf(&b, " (tags: %s)", strings.Join(hit.Tags, ", "))
		}
		b.WriteByte('\n')
		if hit.Snippet != "" {
			fmt.Fprintf(&b, "  %q\n", hit.Snippet)
		}
	}
	if partial != nil {
		fmt.Fprintf(&b, "%s.\n", partial.Error())
	}
	if len(result.Hits) == 0 {
		b.WriteString("No catalog entries match. Try find for names, a broader subject, or full_text.\n")
	}
	return nibagent.ToolResult{Content: b.String()}
}
