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

// versionsTool lists a document's editions (Catalog.Versions), so the
// librarian can say what changed and when by opening path/v<N>.
type versionsTool struct {
	catalog      port.Catalog
	defaultWorld string
}

func (t *versionsTool) Definition() llm.ToolDef {
	return llm.ToolDef{Type: "function", Function: llm.FunctionDef{
		Name:        "versions",
		Description: "List a document's editions, newest first, with when each was published. Open path/v<N> to read an edition and compare.",
		Parameters: llm.FunctionParams{Type: "object", Properties: map[string]llm.FunctionParam{
			"path":  {Type: "string", Description: "document path within the world"},
			"world": {Type: "string", Description: "world the document lives in (default: the reading room's default world)"},
		}, Required: []string{"path"}},
	}}
}

func (t *versionsTool) step(args string) domain.LibrarianStep {
	ref, _, ok := docRef(args, t.defaultWorld)
	if !ok {
		return domain.LibrarianStep{}
	}
	return domain.LibrarianStep{Text: "listed the editions of", Ref: ref}
}

func (t *versionsTool) Execute(ctx context.Context, call llm.ToolCall) nibagent.ToolResult {
	if err := decodeArgs(call.Function.Arguments, &docArgs{}); err != nil {
		return errResult(err)
	}
	ref, _, ok := docRef(call.Function.Arguments, t.defaultWorld)
	if !ok {
		return errResult(errors.New("versions: path is required"))
	}
	raw, err := t.catalog.Versions(ctx, ref.World, ref.Path)
	if err != nil {
		return errResult(err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "mark://%s%s\n", ref.World, ref.Path)
	b.WriteString(strings.TrimSpace(raw.Body))
	fmt.Fprintf(&b, "\n\nopen %s/v<N> to read an edition\n", ref.Path)
	return nibagent.ToolResult{Content: b.String()}
}
