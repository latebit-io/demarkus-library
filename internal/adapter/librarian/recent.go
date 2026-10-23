package librarian

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus-library/internal/core/port"
	nibagent "github.com/latebit-io/nib/agent"
	"github.com/latebit-io/nib/ai/llm"
)

const (
	// defaultRecentDays is the window when the model names none.
	defaultRecentDays = 7
	// maxRecentDays bounds the window; past a year "recent" means the catalog.
	maxRecentDays = 365
)

// recentTool answers "what changed": the catalog narrowed to documents
// modified inside a window (Catalog.Lookup with Since), optionally by world,
// tag, or subject.
type recentTool struct {
	catalog port.Catalog
	now     func() time.Time
}

type recentArgs struct {
	Days  int    `json:"days"`
	World string `json:"world"`
	Tag   string `json:"tag"`
	Query string `json:"query"`
}

func (t *recentTool) Definition() llm.ToolDef {
	return llm.ToolDef{Type: "function", Function: llm.FunctionDef{
		Name:        "recent",
		Description: "List documents modified in the last N days, most important first. Narrow by world, tag, or subject. The list carries no dates — call versions on a document for when each edition was published.",
		Parameters: llm.FunctionParams{Type: "object", Properties: map[string]llm.FunctionParam{
			"days":  {Type: "integer", Description: "window in days (default 7, at most 365)"},
			"world": {Type: "string", Description: "restrict to one world (default: all worlds)"},
			"tag":   {Type: "string", Description: "only documents carrying this tag"},
			"query": {Type: "string", Description: "subject to rank by, matched against tags and titles"},
		}, Required: []string{}},
	}}
}

func (t *recentTool) step(args string) domain.LibrarianStep {
	var in recentArgs
	if decodeArgs(args, &in) != nil {
		return domain.LibrarianStep{}
	}
	return domain.LibrarianStep{Text: fmt.Sprintf("checked what changed in the last %s%s", dayWord(windowDays(in.Days)), inWorld(in.World))}
}

func (t *recentTool) Execute(ctx context.Context, call llm.ToolCall) nibagent.ToolResult {
	var in recentArgs
	if err := decodeArgs(call.Function.Arguments, &in); err != nil {
		return errResult(err)
	}
	days := windowDays(in.Days)
	since := t.now().Add(-time.Duration(days) * 24 * time.Hour)
	result, err := t.catalog.Lookup(ctx, domain.CatalogQuery{
		World: in.World,
		Query: strings.TrimSpace(in.Query),
		Tag:   cleanTag(in.Tag),
		Since: since,
		Limit: maxLookupHits,
	})
	var partial *domain.PartialLookupError
	if err != nil && !errors.As(err, &partial) {
		return errResult(err)
	}
	var b strings.Builder
	if len(result.Hits) == 0 {
		fmt.Fprintf(&b, "Nothing changed in the last %s%s.\n", dayWord(days), inWorld(in.World))
	} else {
		fmt.Fprintf(&b, "Modified since %s, most important first (no per-document dates here — versions has them):\n", since.UTC().Format("2006-01-02"))
		writeHits(&b, result.Hits)
	}
	if partial != nil {
		fmt.Fprintf(&b, "%s.\n", partial.Error())
	}
	return nibagent.ToolResult{Content: b.String()}
}

// windowDays clamps the model's window to [1, maxRecentDays], defaulting when unset.
func windowDays(days int) int {
	return min(positiveOr(days, defaultRecentDays), maxRecentDays)
}

// dayWord phrases a day count for a trace line.
func dayWord(days int) string {
	if days == 1 {
		return "day"
	}
	return fmt.Sprintf("%d days", days)
}
