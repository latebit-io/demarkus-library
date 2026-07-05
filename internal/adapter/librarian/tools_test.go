package librarian

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	nibagent "github.com/latebit-io/nib/agent"
	"github.com/latebit-io/nib/ai/llm"
)

func call(name, args string) llm.ToolCall {
	return llm.ToolCall{ID: "t1", Type: "function",
		Function: llm.FunctionCall{Name: name, Arguments: args}}
}

func TestFindTool_FiltersAndScopes(t *testing.T) {
	t.Parallel()

	tool := &findTool{reader: newFakePorts()}
	res := tool.Execute(context.Background(), call("find", `{"query":"deploy"}`))
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "mark://root/ops/deploy.md — Deploy runbook [published]") {
		t.Errorf("missing match line:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "palette") {
		t.Errorf("non-matching entry leaked:\n%s", res.Content)
	}

	res = tool.Execute(context.Background(), call("find", `{"query":"zzz-nothing"}`))
	if res.IsError || !strings.Contains(res.Content, "No titles or paths match") {
		t.Errorf("empty result not reported honestly: %+v", res)
	}
}

func TestFindTool_RequiresQuery(t *testing.T) {
	t.Parallel()

	tool := &findTool{reader: newFakePorts()}
	if res := tool.Execute(context.Background(), call("find", `{}`)); !res.IsError {
		t.Errorf("missing query accepted: %+v", res)
	}
}

func TestOpenTool_DefaultWorldMetadataAndTruncation(t *testing.T) {
	t.Parallel()

	ports := newFakePorts()
	ports.raw.Body = strings.Repeat("x", maxOpenBytes+500)
	tool := &openTool{reader: ports, defaultWorld: "root"}

	res := tool.Execute(context.Background(), call("open", `{"path":"/ops/deploy.md","force":true}`))
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if !strings.HasPrefix(res.Content, "mark://root/ops/deploy.md\n") {
		t.Errorf("missing address header:\n%.120s", res.Content)
	}
	if !strings.Contains(res.Content, "title: Deploy runbook") {
		t.Errorf("missing metadata line:\n%.200s", res.Content)
	}
	if !strings.Contains(res.Content, "[truncated — 500 more bytes]") {
		t.Errorf("oversized body not truncated with a note")
	}
	if got := ports.rawCalls(); len(got) != 1 || got[0] != "root:/ops/deploy.md" {
		t.Errorf("Raw calls = %v; want default world applied", got)
	}
}

func TestOpenTool_TruncationIsRuneSafe(t *testing.T) {
	t.Parallel()

	ports := newFakePorts()
	// Fill so a multi-byte rune straddles the cut boundary.
	ports.raw.Body = strings.Repeat("x", maxOpenBytes-1) + "→ tail"
	tool := &openTool{reader: ports, defaultWorld: "root"}

	res := tool.Execute(context.Background(), call("open", `{"path":"/ops/deploy.md","force":true}`))
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if !utf8.ValidString(res.Content) {
		t.Error("truncated body is not valid UTF-8")
	}
	if strings.Contains(res.Content, "�") {
		t.Error("truncation produced a replacement character")
	}
}

func TestTools_MalformedArgsErrorNotPanic(t *testing.T) {
	t.Parallel()

	ports := newFakePorts()
	for _, tc := range []struct {
		name string
		res  nibagent.ToolResult
	}{
		{"find", (&findTool{reader: ports}).Execute(context.Background(), call("find", `{"query": 42}`))},
		{"open", (&openTool{reader: ports, defaultWorld: "root"}).Execute(context.Background(), call("open", `not-json`))},
		{"links", (&linksTool{graph: ports, defaultWorld: "root"}).Execute(context.Background(), call("links", `[1,2]`))},
	} {
		if !tc.res.IsError {
			t.Errorf("%s: malformed args accepted: %+v", tc.name, tc.res)
		}
	}
}

func TestLinksTool_EmptyIsHonest(t *testing.T) {
	t.Parallel()

	ports := newFakePorts()
	ports.hood.Out = nil
	tool := &linksTool{graph: ports, defaultWorld: "root"}
	res := tool.Execute(context.Background(), call("links", `{"path":"/lonely.md"}`))
	if res.IsError || !strings.Contains(res.Content, "No edges observed yet") {
		t.Errorf("cold state not honest: %+v", res)
	}
}

func TestWorldsTool_RendersWorldsAndDocs(t *testing.T) {
	t.Parallel()

	tool := &worldsTool{maps: newFakePorts()}
	res := tool.Execute(context.Background(), call("worlds", ""))
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	for _, want := range []string{"world root", "/index.md — root hub [published]"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("missing %q:\n%s", want, res.Content)
		}
	}
}

func TestCompactArgs(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ in, want string }{
		{`{"query":"deploy","world":"root"}`, `query="deploy" world="root"`},
		{`{}`, ""},
		{"", ""},
		{`{"n": 3}`, "n=3"},
	} {
		if got := compactArgs(tc.in); got != tc.want {
			t.Errorf("compactArgs(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
	if got := compactArgs("{broken"); got != "{broken" {
		t.Errorf("unparseable args should pass through; got %q", got)
	}
}

const outlineTestDoc = `# Big Doc

Opening paragraph of the big doc.

## Setup

Setup body.

## Usage

Usage body.
`

// bigStructuredDoc pads outlineTestDoc past the outline threshold while
// keeping real headings.
func bigStructuredDoc() string {
	return outlineTestDoc + "\n## Filler\n\n" + strings.Repeat("filler line\n", 900)
}

func TestOpenTool_LargeDocReturnsOutline(t *testing.T) {
	t.Parallel()

	ports := newFakePorts()
	ports.raw.Body = bigStructuredDoc()
	tool := &openTool{reader: ports, defaultWorld: "root"}

	res := tool.Execute(context.Background(), call("open", `{"path":"/ops/deploy.md"}`))
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	for _, want := range []string{
		"mode: outline",
		"size: ",
		"- Setup (#setup,",
		"Opening paragraph of the big doc.",
		"open /ops/deploy.md#<anchor> for a section; force for the full body",
	} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("outline missing %q:\n%s", want, res.Content)
		}
	}
	if strings.Contains(res.Content, "filler line") {
		t.Error("outline should not include section bodies")
	}
}

func TestOpenTool_SectionOpen(t *testing.T) {
	t.Parallel()

	ports := newFakePorts()
	ports.raw.Body = bigStructuredDoc()
	tool := &openTool{reader: ports, defaultWorld: "root"}

	res := tool.Execute(context.Background(), call("open", `{"path":"/ops/deploy.md#setup"}`))
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	for _, want := range []string{"section: #setup", "## Setup", "Setup body."} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("section open missing %q:\n%s", want, res.Content)
		}
	}
	if strings.Contains(res.Content, "Usage body.") || strings.Contains(res.Content, "mode: outline") {
		t.Errorf("section open leaked other content:\n%.300s", res.Content)
	}
	if got := ports.rawCalls(); len(got) != 1 || got[0] != "root:/ops/deploy.md" {
		t.Errorf("Raw calls = %v; the #anchor must be stripped before the world read", got)
	}
}

func TestOpenTool_SectionNotFoundListsAnchors(t *testing.T) {
	t.Parallel()

	ports := newFakePorts()
	ports.raw.Body = outlineTestDoc
	tool := &openTool{reader: ports, defaultWorld: "root"}

	res := tool.Execute(context.Background(), call("open", `{"path":"/ops/deploy.md#nope"}`))
	if !res.IsError {
		t.Fatal("missing section should be a tool error")
	}
	if !strings.Contains(res.Content, "available anchors") || !strings.Contains(res.Content, "setup") {
		t.Errorf("error should list available anchors:\n%s", res.Content)
	}
}

func TestOpenTool_SmallDocKeepsFullBody(t *testing.T) {
	t.Parallel()

	ports := newFakePorts()
	ports.raw.Body = outlineTestDoc
	tool := &openTool{reader: ports, defaultWorld: "root"}

	res := tool.Execute(context.Background(), call("open", `{"path":"/ops/deploy.md"}`))
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "Setup body.") || !strings.Contains(res.Content, "Usage body.") {
		t.Errorf("small doc should return the full body:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "mode: outline") {
		t.Error("small doc should not be outlined")
	}
}

func TestOpenTool_ForceReturnsFullBody(t *testing.T) {
	t.Parallel()

	ports := newFakePorts()
	ports.raw.Body = bigStructuredDoc()
	tool := &openTool{reader: ports, defaultWorld: "root"}

	res := tool.Execute(context.Background(), call("open", `{"path":"/ops/deploy.md","force":true}`))
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if strings.Contains(res.Content, "mode: outline") {
		t.Error("force should bypass outline mode")
	}
	if !strings.Contains(res.Content, "filler line") {
		t.Error("force should return the body")
	}
}

func TestOpenTool_HugeOutlineKeepsFooter(t *testing.T) {
	t.Parallel()

	// Enough headings that the rendered outline alone exceeds the 16KB
	// cap: the tree gets truncated, the navigation footer must survive.
	var doc strings.Builder
	doc.WriteString("# Monster\n\nOpening paragraph.\n\n")
	for i := range 1200 {
		fmt.Fprintf(&doc, "## Section heading number %d with some length to it\n\nbody\n\n", i)
	}
	ports := newFakePorts()
	ports.raw.Body = doc.String()
	tool := &openTool{reader: ports, defaultWorld: "root"}

	res := tool.Execute(context.Background(), call("open", `{"path":"/ops/deploy.md"}`))
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "[outline truncated —") {
		t.Error("oversized outline should carry its own truncation note")
	}
	if !strings.HasSuffix(strings.TrimRight(res.Content, "\n"), "open /ops/deploy.md#<anchor> for a section; force for the full body") {
		t.Errorf("navigation footer must survive outline truncation; tail:\n%s", res.Content[len(res.Content)-200:])
	}
	if strings.Contains(res.Content, "\n\n[truncated —") {
		t.Error("generic truncation must not fire on outline mode")
	}
	if !utf8.ValidString(res.Content) {
		t.Error("truncated outline is not valid UTF-8")
	}
}
