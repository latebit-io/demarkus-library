package librarian

import (
	"context"
	"strings"
	"testing"

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

	res := tool.Execute(context.Background(), call("open", `{"path":"/ops/deploy.md"}`))
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
