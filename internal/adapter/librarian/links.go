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
	"golang.org/x/net/html"
)

// linksTool traces a document's neighborhood: observed outbound links and
// backlinks (GraphService.Neighborhood). The graph fills as the room is read,
// so a document nobody has rendered yet falls back to reading its own links.
type linksTool struct {
	graph        port.GraphService
	reader       port.Reader
	defaultWorld string
}

func (t *linksTool) Definition() llm.ToolDef {
	return llm.ToolDef{Type: "function", Function: llm.FunctionDef{
		Name:        "links",
		Description: "Show the documents a document links to and the documents observed linking to it (backlinks). Backlinks fill as the room is read — none listed means none observed yet, not none at all.",
		Parameters: llm.FunctionParams{Type: "object", Properties: map[string]llm.FunctionParam{
			"path":  {Type: "string", Description: "document path within the world"},
			"world": {Type: "string", Description: "world the document lives in (default: the reading room's default world)"},
		}, Required: []string{"path"}},
	}}
}

func (t *linksTool) step(args string) domain.LibrarianStep {
	ref, _, ok := docRef(args, t.defaultWorld)
	if !ok {
		return domain.LibrarianStep{}
	}
	return domain.LibrarianStep{Text: "traced the links of", Ref: ref}
}

func (t *linksTool) Execute(ctx context.Context, call llm.ToolCall) nibagent.ToolResult {
	if err := decodeArgs(call.Function.Arguments, &docArgs{}); err != nil {
		return errResult(err)
	}
	ref, _, ok := docRef(call.Function.Arguments, t.defaultWorld)
	if !ok {
		return errResult(errors.New("links: path is required"))
	}
	if domain.IsListingPath(ref.Path) {
		return errResult(fmt.Errorf("links: %s is a listing, not a document — find its documents instead", ref.Path))
	}

	hood := t.graph.Neighborhood(ref.World, ref.Path)
	outbound, observed := hood.Out, true
	if len(outbound) == 0 {
		// Nothing observed yet: read the document's own links. A failed read
		// leaves the observed (empty) answer standing rather than failing.
		if doc, err := t.reader.ReadCached(ctx, ref.World, ref.Path); err == nil {
			outbound, observed = outboundRefs(doc.HTML, ref.World, doc.Path), false
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "mark://%s%s\n", ref.World, ref.Path)
	if len(outbound) == 0 && len(hood.In) == 0 {
		b.WriteString("No links found and no backlinks observed yet.\n")
		return nibagent.ToolResult{Content: b.String()}
	}
	if len(outbound) > 0 {
		if observed {
			b.WriteString("links to:\n")
		} else {
			b.WriteString("links to (read from the document):\n")
		}
		for _, r := range outbound {
			fmt.Fprintf(&b, "  mark://%s%s\n", r.World, r.Path)
		}
	}
	if len(hood.In) > 0 {
		b.WriteString("referenced by:\n")
		for _, r := range hood.In {
			fmt.Fprintf(&b, "  mark://%s%s\n", r.World, r.Path)
		}
	}
	return nibagent.ToolResult{Content: b.String()}
}

// outboundRefs lists the in-universe documents a rendered document links to,
// first-seen order, each once. Anchors, listings, and external links are not
// document links.
func outboundRefs(fragment, world, basePath string) []domain.Ref {
	root, err := html.Parse(strings.NewReader(fragment))
	if err != nil {
		return nil
	}
	var refs []domain.Ref
	seen := map[domain.Ref]bool{}
	for n := range root.Descendants() {
		if n.Type != html.ElementNode || n.Data != "a" {
			continue
		}
		for _, attr := range n.Attr {
			if attr.Key != "href" {
				continue
			}
			target, ok := domain.ResolveHref(attr.Val, world, basePath)
			if ok && !domain.IsListingPath(target.Path) && !seen[target.Ref] {
				seen[target.Ref] = true
				refs = append(refs, target.Ref)
			}
		}
	}
	return refs
}
