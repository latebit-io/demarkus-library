package broker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/latebit/demarkus-library/internal/adapter/bearer"
	"github.com/latebit/demarkus-library/internal/core/domain"
)

// fakeCaller scripts one tool response and records what the gateway sent.
type fakeCaller struct {
	text      string
	isToolErr bool
	err       error

	gotToken string
	gotTool  string
	gotArgs  map[string]any
}

func (f *fakeCaller) callTool(_ context.Context, token, tool string, args map[string]any) (string, bool, error) {
	f.gotToken, f.gotTool, f.gotArgs = token, tool, args
	return f.text, f.isToolErr, f.err
}

func authedCtx(t *testing.T) context.Context {
	t.Helper()
	return bearer.WithToken(t.Context(), "tok-123")
}

func TestFetchParsesResult(t *testing.T) {
	fc := &fakeCaller{text: "status: ok\nversion: 3\ntitle: Hello\n\n# Hello\n\nbody text"}
	g := &Gateway{caller: fc, world: "soul"}

	raw, err := g.Fetch(authedCtx(t), "/index.md")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if fc.gotToken != "tok-123" {
		t.Errorf("token = %q", fc.gotToken)
	}
	if fc.gotTool != "mark_fetch" {
		t.Errorf("tool = %q", fc.gotTool)
	}
	if got := fc.gotArgs["url"]; got != "mark://soul/index.md" {
		t.Errorf("url arg = %v", got)
	}
	if raw.Source != "soul" || raw.Path != "/index.md" {
		t.Errorf("raw = %+v", raw)
	}
	// Body must survive intact, including its own blank lines.
	if raw.Body != "# Hello\n\nbody text" {
		t.Errorf("body = %q", raw.Body)
	}
	if raw.Metadata["version"] != "3" || raw.Metadata["title"] != "Hello" {
		t.Errorf("metadata = %v", raw.Metadata)
	}
}

func TestVerbsAndArgs(t *testing.T) {
	cases := []struct {
		name     string
		call     func(g *Gateway, ctx context.Context) (domain.RawDocument, error)
		wantTool string
		wantURL  string
	}{
		{"List", func(g *Gateway, ctx context.Context) (domain.RawDocument, error) { return g.List(ctx, "/plans/") },
			"mark_list", "mark://soul/plans/"},
		{"Versions", func(g *Gateway, ctx context.Context) (domain.RawDocument, error) { return g.Versions(ctx, "/x.md") },
			"mark_versions", "mark://soul/x.md"},
		{"Lookup", func(g *Gateway, ctx context.Context) (domain.RawDocument, error) { return g.Lookup(ctx, "/", "hex") },
			"mark_lookup", "mark://soul/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := &fakeCaller{text: "status: ok\n\nbody"}
			g := &Gateway{caller: fc, world: "soul"}
			if _, err := tc.call(g, authedCtx(t)); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if fc.gotTool != tc.wantTool {
				t.Errorf("tool = %q, want %q", fc.gotTool, tc.wantTool)
			}
			if got := fc.gotArgs["url"]; got != tc.wantURL {
				t.Errorf("url = %v, want %q", got, tc.wantURL)
			}
		})
	}

	// Lookup carries the query argument too.
	fc := &fakeCaller{text: "status: ok\n\nbody"}
	g := &Gateway{caller: fc, world: "soul"}
	if _, err := g.Lookup(authedCtx(t), "/", "hexagonal"); err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got := fc.gotArgs["query"]; got != "hexagonal" {
		t.Errorf("query arg = %v", got)
	}
}

func TestStatusMapping(t *testing.T) {
	cases := []struct {
		status string
		want   error
	}{
		{"not-found", domain.ErrNotFound},
		{"archived", domain.ErrNotFound},
		{"unauthorized", domain.ErrUnauthorized},
		{"not-permitted", domain.ErrUnauthorized},
	}
	for _, tc := range cases {
		fc := &fakeCaller{text: "status: " + tc.status + "\n"}
		g := &Gateway{caller: fc, world: "soul"}
		if _, err := g.Fetch(authedCtx(t), "/x.md"); !errors.Is(err, tc.want) {
			t.Errorf("status %s: err = %v, want %v", tc.status, err, tc.want)
		}
	}

	// Unknown status is an explicit failure, not a silent empty document.
	fc := &fakeCaller{text: "status: weird\n"}
	g := &Gateway{caller: fc, world: "soul"}
	if _, err := g.Fetch(authedCtx(t), "/x.md"); err == nil {
		t.Error("unknown status accepted")
	}
}

func TestNoBearerIsUnauthorized(t *testing.T) {
	fc := &fakeCaller{}
	g := &Gateway{caller: fc, world: "soul"}
	if _, err := g.Fetch(t.Context(), "/x.md"); !errors.Is(err, domain.ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
	if fc.gotTool != "" {
		t.Error("gateway called the broker without a bearer")
	}
}

func TestToolErrorMapping(t *testing.T) {
	// Broker-level isError payloads (toolErrorFor).
	fc := &fakeCaller{text: `not authorized for world "soul"`, isToolErr: true}
	g := &Gateway{caller: fc, world: "soul"}
	if _, err := g.Fetch(authedCtx(t), "/x.md"); !errors.Is(err, domain.ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}

	fc = &fakeCaller{text: "fetch failed: world dial timeout", isToolErr: true}
	g = &Gateway{caller: fc, world: "soul"}
	_, err := g.Fetch(authedCtx(t), "/x.md")
	if err == nil || errors.Is(err, domain.ErrUnauthorized) || errors.Is(err, domain.ErrNotFound) {
		t.Errorf("err = %v, want generic broker error", err)
	}
}

func TestTransportAuthRejection(t *testing.T) {
	// gatewayAuth 401s surface from mcp-go as the typed
	// transport.ErrUnauthorized sentinel (possibly wrapped); they must
	// become ErrUnauthorized (login redirect), not a 502.
	fc := &fakeCaller{err: fmt.Errorf("initialize: %w", transport.ErrUnauthorized)}
	g := &Gateway{caller: fc, world: "soul"}
	if _, err := g.Fetch(authedCtx(t), "/x.md"); !errors.Is(err, domain.ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}

	// A non-auth transport failure must NOT redirect to login — including
	// messages that merely contain 401-ish substrings (e.g. port 4010).
	fc = &fakeCaller{err: errors.New("dial tcp 10.0.0.1:4010: connection refused")}
	g = &Gateway{caller: fc, world: "soul"}
	if _, err := g.Fetch(authedCtx(t), "/x.md"); errors.Is(err, domain.ErrUnauthorized) {
		t.Error("transport failure misread as auth rejection")
	}
}

// TestEndToEndAgainstMCPServer drives the production mcpCaller against a real
// mcp-go StreamableHTTPServer — the same server type the broker wraps — with
// a bearer-checking middleware in front, mirroring gatewayAuth.
func TestEndToEndAgainstMCPServer(t *testing.T) {
	srv := mcpserver.NewMCPServer("test-broker", "0.0.1", mcpserver.WithToolCapabilities(false))
	srv.AddTool(
		mcp.NewTool("mark_fetch", mcp.WithString("url", mcp.Required())),
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			url, _ := req.RequireString("url")
			if url != "mark://soul/index.md" {
				return mcp.NewToolResultError("unexpected url: " + url), nil
			}
			return mcp.NewToolResultText("status: ok\ntitle: Soul\n\n# Soul hub"), nil
		},
	)
	streamable := mcpserver.NewStreamableHTTPServer(srv)

	gate := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good-token" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="test"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		streamable.ServeHTTP(w, r)
	})
	ts := httptest.NewServer(gate)
	defer ts.Close()

	// The production gateway: NewGateway points the mcpCaller at /mcp, but
	// the test server serves MCP at its root — point the caller directly.
	g := &Gateway{caller: &mcpCaller{mcpURL: ts.URL}, world: "soul"}

	raw, err := g.Fetch(bearer.WithToken(t.Context(), "good-token"), "/index.md")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if raw.Body != "# Soul hub" || raw.Metadata["title"] != "Soul" {
		t.Errorf("raw = %+v", raw)
	}

	// A rejected bearer maps to ErrUnauthorized through the real transport.
	if _, err := g.Fetch(bearer.WithToken(t.Context(), "bad-token"), "/index.md"); !errors.Is(err, domain.ErrUnauthorized) {
		t.Errorf("bad bearer: err = %v, want ErrUnauthorized", err)
	}
}
