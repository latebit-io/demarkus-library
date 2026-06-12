package broker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

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
	g := &Gateway{caller: fc}

	raw, err := g.Fetch(authedCtx(t), "soul", "/index.md")
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
		{"List", func(g *Gateway, ctx context.Context) (domain.RawDocument, error) { return g.List(ctx, "soul", "/plans/") },
			"mark_list", "mark://soul/plans/"},
		{"Versions", func(g *Gateway, ctx context.Context) (domain.RawDocument, error) { return g.Versions(ctx, "soul", "/x.md") },
			"mark_versions", "mark://soul/x.md"},
		{"Lookup", func(g *Gateway, ctx context.Context) (domain.RawDocument, error) { return g.Lookup(ctx, "soul", "/", "hex") },
			"mark_lookup", "mark://soul/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := &fakeCaller{text: "status: ok\n\nbody"}
			g := &Gateway{caller: fc}
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
	g := &Gateway{caller: fc}
	if _, err := g.Lookup(authedCtx(t), "soul", "/", "hexagonal"); err != nil {
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
		g := &Gateway{caller: fc}
		if _, err := g.Fetch(authedCtx(t), "soul", "/x.md"); !errors.Is(err, tc.want) {
			t.Errorf("status %s: err = %v, want %v", tc.status, err, tc.want)
		}
	}

	// Unknown status is an explicit failure, not a silent empty document.
	fc := &fakeCaller{text: "status: weird\n"}
	g := &Gateway{caller: fc}
	if _, err := g.Fetch(authedCtx(t), "soul", "/x.md"); err == nil {
		t.Error("unknown status accepted")
	}
}

func TestNoBearerIsUnauthorized(t *testing.T) {
	fc := &fakeCaller{}
	g := &Gateway{caller: fc}
	if _, err := g.Fetch(t.Context(), "soul", "/x.md"); !errors.Is(err, domain.ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
	if fc.gotTool != "" {
		t.Error("gateway called the broker without a bearer")
	}
}

func TestToolErrorMapping(t *testing.T) {
	// Broker-level isError payloads (toolErrorFor).
	fc := &fakeCaller{text: `not authorized for world "soul"`, isToolErr: true}
	g := &Gateway{caller: fc}
	if _, err := g.Fetch(authedCtx(t), "soul", "/x.md"); !errors.Is(err, domain.ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}

	fc = &fakeCaller{text: "fetch failed: world dial timeout", isToolErr: true}
	g = &Gateway{caller: fc}
	_, err := g.Fetch(authedCtx(t), "soul", "/x.md")
	if err == nil || errors.Is(err, domain.ErrUnauthorized) || errors.Is(err, domain.ErrNotFound) {
		t.Errorf("err = %v, want generic broker error", err)
	}
}

func TestTransportAuthRejection(t *testing.T) {
	// gatewayAuth 401s surface from mcp-go as the typed
	// transport.ErrUnauthorized sentinel (possibly wrapped); they must
	// become ErrUnauthorized (login redirect), not a 502.
	fc := &fakeCaller{err: fmt.Errorf("initialize: %w", transport.ErrUnauthorized)}
	g := &Gateway{caller: fc}
	if _, err := g.Fetch(authedCtx(t), "soul", "/x.md"); !errors.Is(err, domain.ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}

	// A non-auth transport failure must NOT redirect to login — including
	// messages that merely contain 401-ish substrings (e.g. port 4010).
	fc = &fakeCaller{err: errors.New("dial tcp 10.0.0.1:4010: connection refused")}
	g = &Gateway{caller: fc}
	if _, err := g.Fetch(authedCtx(t), "soul", "/x.md"); errors.Is(err, domain.ErrUnauthorized) {
		t.Error("transport failure misread as auth rejection")
	}
}

// mcpTestServer is a real mcp-go StreamableHTTPServer — the same server type
// the broker wraps — behind a bearer-checking gate mirroring gatewayAuth. It
// counts initialize requests and can be "restarted" (fresh server-side
// session state) to exercise the pool's rebuild path.
type mcpTestServer struct {
	ts *httptest.Server

	mu          sync.Mutex
	streamable  *mcpserver.StreamableHTTPServer
	initializes int
	failNext    bool // 404 the next tools/call once (simulated session loss)
}

func newMCPTestServer(t *testing.T) *mcpTestServer {
	t.Helper()
	m := &mcpTestServer{}
	m.restart()

	gate := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good-token" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="test"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		m.mu.Lock()
		if bytes.Contains(body, []byte(`"method":"initialize"`)) {
			m.initializes++
		}
		if m.failNext && bytes.Contains(body, []byte(`"method":"tools/call"`)) {
			m.failNext = false
			m.mu.Unlock()
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		streamable := m.streamable
		m.mu.Unlock()
		streamable.ServeHTTP(w, r)
	})
	m.ts = httptest.NewServer(gate)
	t.Cleanup(m.ts.Close)
	return m
}

// restart swaps in a fresh streamable server: all session ids the client
// holds become unknown, exactly as after a broker redeploy.
func (m *mcpTestServer) restart() {
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
	m.mu.Lock()
	m.streamable = mcpserver.NewStreamableHTTPServer(srv)
	m.mu.Unlock()
}

func (m *mcpTestServer) initializeCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.initializes
}

// gateway builds the production Gateway against the test server. NewGateway
// appends /mcp; the test server serves MCP at its root, so wire the caller
// directly.
func (m *mcpTestServer) gateway() *Gateway {
	return &Gateway{
		caller: &mcpCaller{mcpURL: m.ts.URL, now: time.Now, pool: make(map[string]*pooledEntry)},
	}
}

// TestEndToEndSessionReuse drives the production mcpCaller end to end and
// pins the pool's reason for existing: N reads, one initialize.
func TestEndToEndSessionReuse(t *testing.T) {
	srv := newMCPTestServer(t)
	g := srv.gateway()
	defer g.Close()
	ctx := bearer.WithToken(t.Context(), "good-token")

	for range 5 {
		raw, err := g.Fetch(ctx, "soul", "/index.md")
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if raw.Body != "# Soul hub" || raw.Metadata["title"] != "Soul" {
			t.Fatalf("raw = %+v", raw)
		}
	}
	if n := srv.initializeCount(); n != 1 {
		t.Errorf("initialize count = %d after 5 reads, want 1 (session reused)", n)
	}

	// A rejected bearer maps to ErrUnauthorized through the real transport.
	if _, err := g.Fetch(bearer.WithToken(t.Context(), "bad-token"), "soul", "/index.md"); !errors.Is(err, domain.ErrUnauthorized) {
		t.Errorf("bad bearer: err = %v, want ErrUnauthorized", err)
	}
}

// TestEndToEndSessionLost 404s one tools/call mid-stream (the shape of a
// server-side session loss); the pool must invalidate, rebuild, and retry
// transparently — the reader sees a successful read, not an error page.
//
// Note a plain broker redeploy does NOT invalidate sessions: mcp-go's
// default session manager validates id format, not existence. This guards
// the rebuild path for the failures that do occur (LB resets, future
// stateful session managers).
func TestEndToEndSessionLost(t *testing.T) {
	srv := newMCPTestServer(t)
	g := srv.gateway()
	defer g.Close()
	ctx := bearer.WithToken(t.Context(), "good-token")

	if _, err := g.Fetch(ctx, "soul", "/index.md"); err != nil {
		t.Fatalf("first Fetch: %v", err)
	}
	srv.mu.Lock()
	srv.failNext = true
	srv.mu.Unlock()

	raw, err := g.Fetch(ctx, "soul", "/index.md")
	if err != nil {
		t.Fatalf("Fetch after session loss: %v", err)
	}
	if raw.Body != "# Soul hub" {
		t.Errorf("raw = %+v", raw)
	}
	if n := srv.initializeCount(); n != 2 {
		t.Errorf("initialize count = %d, want 2 (rebuild after loss)", n)
	}
}

// TestEndToEndConcurrentSingleInitialize hammers a cold pool from many
// goroutines; the entry lock must single-flight the handshake.
func TestEndToEndConcurrentSingleInitialize(t *testing.T) {
	srv := newMCPTestServer(t)
	g := srv.gateway()
	defer g.Close()
	ctx := bearer.WithToken(t.Context(), "good-token")

	const readers = 8
	var wg sync.WaitGroup
	errs := make([]error, readers)
	for i := range readers {
		wg.Go(func() {
			_, errs[i] = g.Fetch(ctx, "soul", "/index.md")
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("reader %d: %v", i, err)
		}
	}
	if n := srv.initializeCount(); n != 1 {
		t.Errorf("initialize count = %d under %d concurrent readers, want 1", n, readers)
	}
}

// TestPoolEviction pins the lazy lifecycle: idle entries vanish after
// idleTTL, and the cap holds under bearer churn.
func TestPoolEviction(t *testing.T) {
	clock := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	caller := &mcpCaller{mcpURL: "http://unused.invalid", now: func() time.Time { return clock }, pool: make(map[string]*pooledEntry)}

	caller.entryFor("bearer-a")
	clock = clock.Add(idleTTL + time.Minute)
	caller.entryFor("bearer-b") // sweep runs here

	caller.mu.Lock()
	_, aLives := caller.pool["bearer-a"]
	size := len(caller.pool)
	caller.mu.Unlock()
	if aLives || size != 1 {
		t.Errorf("idle entry survived: aLives=%v size=%d", aLives, size)
	}

	// Cap: maxPool fresh bearers, then one more — size must not exceed maxPool.
	for i := range maxPool {
		caller.entryFor(fmt.Sprintf("bearer-%d", i))
	}
	caller.entryFor("bearer-overflow")
	caller.mu.Lock()
	size = len(caller.pool)
	caller.mu.Unlock()
	if size > maxPool {
		t.Errorf("pool size = %d, want <= %d", size, maxPool)
	}
}
