// Package broker is the outbound adapter that implements port.WorldGateway
// over the broker's MCP gateway (/mcp, Streamable HTTP) — the Phase 1b
// transport (ADR 0004). Where the world adapter dials a single world over
// QUIC with a static token, this one presents the logged-in reader's bearer
// (from the request context via the bearer package) and reads through the
// broker's mark_* tools.
//
// Connection model: one pooled MCP session per reader, keyed by bearer. The
// stateless-per-call model this replaced made ~3 HTTP requests per read
// (initialize → initialized → tools/call) and tripped the broker's
// per-subject rate limiter on the first live e2e (see debugging notes). The
// pool keeps a reader's initialized session across reads — one request per
// read after the first — while sessions stay identity-aligned: no MCP
// session is ever shared across bearers (a shared session with per-request
// header injection would work for reads today, but is exactly the identity
// sloppiness that turns into a bug when writes arrive). Token refresh
// rotates the bearer and so naturally retires entries; idle entries evict
// lazily; a failed call rebuilds the session once, transparently covering
// broker restarts.
package broker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/latebit-io/demarkus-library/internal/adapter/bearer"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus-library/internal/core/port"
	"github.com/latebit-io/demarkus/protocol"
)

// toolCaller is the slice of MCP the gateway needs: call one tool with one
// bearer, get the text result back. An interface so tests fake the transport
// without a live broker; production uses mcpCaller.
type toolCaller interface {
	// callTool returns the tool's text payload and whether the tool
	// flagged it as an error (MCP isError — the broker's "world said no"
	// channel, distinct from transport failure).
	callTool(ctx context.Context, token, tool string, args map[string]any) (text string, isToolError bool, err error)
}

// Gateway adapts the broker MCP gateway to the WorldGateway port. The world
// is a knowledge-system world name, per call; the reader's bearer rides in on
// the request context.
type Gateway struct {
	caller toolCaller
}

// compile-time check that Gateway satisfies the outbound port.
var _ port.WorldGateway = (*Gateway)(nil)

// NewGateway builds the production gateway: brokerURL is the broker origin
// (https://broker.example.org). A nil httpClient gets a 15-second-timeout
// default — safe to bound at the client level because no long-lived SSE
// stream is held open; without it a wedged broker would pin request handlers
// indefinitely.
func NewGateway(brokerURL string, httpClient *http.Client) *Gateway {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Gateway{
		caller: &mcpCaller{
			mcpURL: strings.TrimRight(brokerURL, "/") + "/mcp",
			http:   httpClient,
			now:    time.Now,
			pool:   make(map[string]*pooledEntry),
		},
	}
}

// Close shuts down all pooled MCP sessions. Called at process shutdown.
func (g *Gateway) Close() {
	if c, ok := g.caller.(*mcpCaller); ok {
		c.close()
	}
}

// Fetch reads a document through mark_fetch.
func (g *Gateway) Fetch(ctx context.Context, world, path string) (domain.RawDocument, error) {
	return g.read(ctx, "mark_fetch", world, path, map[string]any{"url": markURL(world, path)})
}

// List reads a directory listing (the stacks) through mark_list.
func (g *Gateway) List(ctx context.Context, world, path string) (domain.RawDocument, error) {
	return g.read(ctx, "mark_list", world, path, map[string]any{"url": markURL(world, path)})
}

// Versions reads the edition history through mark_versions.
func (g *Gateway) Versions(ctx context.Context, world, path string) (domain.RawDocument, error) {
	return g.read(ctx, "mark_versions", world, path, map[string]any{"url": markURL(world, path)})
}

// Worlds enumerates the knowledge system's worlds through mark_worlds —
// the authorization-filtered list for the reader's bearer. Parses the
// tool's markdown table (| world | url |) into WorldInfo rows.
func (g *Gateway) Worlds(ctx context.Context) ([]domain.WorldInfo, error) {
	token := bearer.FromContext(ctx)
	if token == "" {
		return nil, domain.ErrUnauthorized
	}
	text, isToolError, err := g.caller.callTool(ctx, token, "mark_worlds", nil)
	if err != nil {
		if errors.Is(err, transport.ErrUnauthorized) {
			return nil, domain.ErrUnauthorized
		}
		return nil, fmt.Errorf("broker: mark_worlds: %w", err)
	}
	if isToolError {
		return nil, mapToolError(text)
	}
	return parseWorldsTable(text), nil
}

// parseWorldsTable extracts WorldInfo rows from mark_worlds' markdown
// table. Header and separator rows are skipped by shape (first cell
// "world" or dashes); anything unparseable is dropped rather than failing
// the universe.
func parseWorldsTable(text string) []domain.WorldInfo {
	var out []domain.WorldInfo
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) < 1 {
			continue
		}
		name := strings.TrimSpace(cells[0])
		if name == "" || name == "world" || strings.HasPrefix(name, "-") {
			continue
		}
		url := ""
		if len(cells) > 1 {
			url = strings.TrimSpace(cells[1])
		}
		// The `address` column (internal dial host) was added after the
		// original two-column table; tolerate its absence for older brokers.
		address := ""
		if len(cells) > 2 {
			address = strings.TrimSpace(cells[2])
		}
		out = append(out, domain.WorldInfo{Name: name, URL: url, Address: address})
	}
	return out
}

// Lookup queries the catalog under scope through mark_lookup. A non-empty
// filter rides along as the tool's comma-separated key=value predicate.
func (g *Gateway) Lookup(ctx context.Context, world, scope, query, filter string) (domain.RawDocument, error) {
	args := map[string]any{
		"url":   markURL(world, scope),
		"query": query,
	}
	if filter != "" {
		args["filter"] = filter
	}
	return g.read(ctx, "mark_lookup", world, scope, args)
}

// Publish writes a document through mark_publish — the cataloging desk's write
// path (Phase 3). Metadata travels in the tool's metadata object (never a body
// fence; ADR 0005 decision 11). on_conflict is "fail" so an expected_version
// mismatch returns a strict conflict the desk surfaces as a reload prompt
// (the structural-merge flow is a later slice). Returns the new version.
func (g *Gateway) Publish(ctx context.Context, world, path, body string, meta domain.PublishMeta, expectedVersion int) (domain.PublishResult, error) {
	token := bearer.FromContext(ctx)
	if token == "" {
		return domain.PublishResult{}, domain.ErrUnauthorized
	}
	metadata := map[string]any{}
	if meta.Title != "" {
		metadata["title"] = meta.Title
	}
	if len(meta.Tags) > 0 {
		metadata["tags"] = strings.Join(meta.Tags, ",")
	}
	if meta.Importance != "" {
		metadata["importance"] = meta.Importance
	}
	// on_conflict by intent: a create (version 0) wants a path-taken conflict to
	// be a hard error ("already exists"), so "fail"; an edit (non-zero version)
	// wants the broker to three-way-merge a stale write into a candidate the
	// desk can review, so "merge".
	onConflict := "merge"
	if expectedVersion == 0 {
		onConflict = "fail"
	}
	args := map[string]any{
		"url":              markURL(world, path),
		"body":             body,
		"expected_version": expectedVersion,
		"on_conflict":      onConflict,
	}
	if len(metadata) > 0 {
		args["metadata"] = metadata
	}

	text, isToolError, err := g.caller.callTool(ctx, token, "mark_publish", args)
	if err != nil {
		if errors.Is(err, transport.ErrUnauthorized) {
			return domain.PublishResult{}, domain.ErrUnauthorized
		}
		return domain.PublishResult{}, fmt.Errorf("broker: mark_publish: %w", err)
	}
	if isToolError {
		return domain.PublishResult{}, mapWriteError(text)
	}
	// A merge (on_conflict="merge" against a stale version) comes back as a
	// normal result with "status: merge-candidate", not a tool error; a clean
	// write is "status: ok".
	if cand, ok := parseMergeCandidate(text); ok {
		return domain.PublishResult{Merge: cand}, nil
	}
	return domain.PublishResult{Version: parsePublishedVersion(text)}, nil
}

// parseMergeCandidate decodes the broker's merge-candidate payload (the
// formatMergeOutcome OutcomeCandidate shape): a "status: merge-candidate" head
// with publish-at-version / has-markers lines, a blank line, then the merged
// body. ok is false for any other result (a clean "status: ok" publish).
func parseMergeCandidate(text string) (*domain.MergeCandidate, bool) {
	head, body, found := strings.Cut(text, "\n\n")
	if !found || !strings.Contains(head, "status: merge-candidate") {
		return nil, false
	}
	cand := &domain.MergeCandidate{Body: body}
	for line := range strings.SplitSeq(head, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ": ")
		if !ok {
			continue
		}
		switch key {
		case "publish-at-version":
			if v, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
				cand.PublishAtVersion = v
			}
		case "has-markers":
			cand.HasMarkers = strings.TrimSpace(value) == "true"
		}
	}
	return cand, true
}

// Append concatenates body onto the document through mark_append — the
// cataloging desk's "add to" (Phase 3). expected_version is omitted so the
// broker auto-resolves the current version (additive, low-conflict); metadata
// is preserved. Returns the new version.
func (g *Gateway) Append(ctx context.Context, world, path, body string) (int, error) {
	token := bearer.FromContext(ctx)
	if token == "" {
		return 0, domain.ErrUnauthorized
	}
	args := map[string]any{"url": markURL(world, path), "body": body}
	text, isToolError, err := g.caller.callTool(ctx, token, "mark_append", args)
	if err != nil {
		if errors.Is(err, transport.ErrUnauthorized) {
			return 0, domain.ErrUnauthorized
		}
		return 0, fmt.Errorf("broker: mark_append: %w", err)
	}
	if isToolError {
		return 0, mapWriteError(text)
	}
	return parsePublishedVersion(text), nil
}

// mapWriteError maps mark_publish error payloads to domain errors. A version
// mismatch (the broker's conflict status) becomes ErrConflict so the desk
// prompts a reload; not-authorized stays ErrUnauthorized.
func mapWriteError(text string) error {
	low := strings.ToLower(text)
	switch {
	case strings.Contains(low, "conflict") || strings.Contains(low, "expected version") || strings.Contains(low, "version mismatch"):
		return domain.ErrConflict
	case strings.Contains(low, "not authorized") || strings.Contains(low, "not permitted"):
		return domain.ErrUnauthorized
	default:
		return errors.New("broker write error: " + text)
	}
}

// parsePublishedVersion best-effort reads the new version from the publish
// result ("version: N" line). The desk re-reads the document live after a
// publish, so a missed parse (0) is harmless — the re-read carries the truth.
func parsePublishedVersion(text string) int {
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "version:"); ok {
			if v, err := strconv.Atoi(strings.TrimSpace(rest)); err == nil {
				return v
			}
		}
	}
	return 0
}

// read runs one tool call and maps the outcome into the domain.
func (g *Gateway) read(ctx context.Context, tool, world, path string, args map[string]any) (domain.RawDocument, error) {
	token := bearer.FromContext(ctx)
	if token == "" {
		// No identity on the request: in broker mode every read sits
		// behind the turnstile, so this is a login redirect, not a
		// transport failure.
		return domain.RawDocument{}, domain.ErrUnauthorized
	}

	text, isToolError, err := g.caller.callTool(ctx, token, tool, args)
	if err != nil {
		if errors.Is(err, transport.ErrUnauthorized) {
			// The broker's gatewayAuth refused the bearer (expired or
			// revoked between refresh and use). mcp-go surfaces HTTP
			// 401 as this typed sentinel; map it to the domain's auth
			// error so the web layer re-runs login.
			return domain.RawDocument{}, domain.ErrUnauthorized
		}
		return domain.RawDocument{}, fmt.Errorf("broker: %s: %w", tool, err)
	}
	if isToolError {
		return domain.RawDocument{}, mapToolError(text)
	}
	return parseToolResult(world, path, text)
}

// markURL builds the mark://<world><path> tool argument. path always starts
// with / on this port.
func markURL(world, path string) string {
	return "mark://" + world + path
}

// mapToolError translates the broker's isError text payloads (mcp_tools_read
// toolErrorFor) into domain errors.
func mapToolError(text string) error {
	if strings.Contains(text, "not authorized") {
		return domain.ErrUnauthorized
	}
	return errors.New("broker tool error: " + text)
}

// parseToolResult decodes the broker's text tool payload (formatToolResult):
// a "status: <s>" first line, "key: value" metadata lines, then a blank line
// and the body. Status maps to domain errors exactly like the QUIC world
// adapter — this is the one place broker payloads cross into the domain.
func parseToolResult(world, path, text string) (domain.RawDocument, error) {
	head, body, _ := strings.Cut(text, "\n\n")

	status := ""
	meta := make(map[string]string)
	for i, line := range strings.Split(head, "\n") {
		key, value, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}
		if i == 0 && key == "status" {
			status = value
			continue
		}
		meta[key] = value
	}

	switch status {
	case protocol.StatusOK:
		// fall through
	case protocol.StatusNotFound, protocol.StatusArchived:
		return domain.RawDocument{}, domain.ErrNotFound
	case protocol.StatusUnauthorized, protocol.StatusNotPermitted:
		return domain.RawDocument{}, domain.ErrUnauthorized
	default:
		return domain.RawDocument{}, errors.New("broker returned status: " + status)
	}

	return domain.RawDocument{
		Source:   world,
		Path:     path,
		Body:     body,
		Metadata: meta,
	}, nil
}

// mcpCaller is the production toolCaller: a fresh Streamable HTTP MCP client
// per reader (see the package comment for the pooling rationale).
type mcpCaller struct {
	mcpURL string
	http   *http.Client
	now    func() time.Time

	// mu guards pool and every entry's lastUsed. Entries hold their own
	// mutex for session build/rebuild so a slow initialize for one reader
	// never blocks the pool.
	mu   sync.Mutex
	pool map[string]*pooledEntry
}

// pooledEntry is one reader's MCP session slot. client is nil before the
// first call and after an invalidation; the entry mutex single-flights the
// (re)build so concurrent reads pay one initialize, not N.
type pooledEntry struct {
	mu       sync.Mutex
	client   *mcpclient.Client
	lastUsed time.Time // guarded by mcpCaller.mu
}

const (
	// idleTTL evicts a reader's MCP session after inactivity. Generous
	// relative to page-view cadence; bearers also rotate on token refresh,
	// which retires entries naturally.
	idleTTL = 15 * time.Minute
	// maxPool bounds the pool against bearer churn (each token refresh
	// keys a new entry). Oldest-idle entries go first.
	maxPool = 256
)

// callTool runs one tool call on the reader's pooled MCP session, building
// the session on first use and rebuilding it once if the call fails (broker
// restart invalidates server-side session ids; the retry is transparent).
func (m *mcpCaller) callTool(ctx context.Context, token, tool string, args map[string]any) (string, bool, error) {
	entry := m.entryFor(token)

	callReq := mcp.CallToolRequest{}
	callReq.Params.Name = tool
	callReq.Params.Arguments = args

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		c, err := m.clientOf(ctx, entry, token)
		if err != nil {
			// Initialize failed against a fresh session — nothing to
			// retry; the broker's answer would not change.
			return "", false, fmt.Errorf("initialize mcp session: %w", err)
		}
		res, err := c.CallTool(ctx, callReq)
		if err == nil {
			var sb strings.Builder
			for _, content := range res.Content {
				if tc, ok := content.(mcp.TextContent); ok {
					sb.WriteString(tc.Text)
				}
			}
			return sb.String(), res.IsError, nil
		}
		m.invalidate(entry, c)
		lastErr = fmt.Errorf("call %s: %w", tool, err)
		if ctx.Err() != nil || errors.Is(err, transport.ErrUnauthorized) {
			// Cancelled, or the bearer itself was rejected — a rebuild
			// would re-fail at initialize with the same answer.
			break
		}
	}
	return "", false, lastErr
}

// entryFor returns the pool slot for a bearer, creating it on first use,
// stamping activity, and lazily evicting idle/overflow entries. Eviction
// only touches entries idle past idleTTL, so an in-flight call (which just
// stamped its entry) can never have its client closed underneath it by the
// sweep — only invalidate does that, on the failing client alone.
func (m *mcpCaller) entryFor(token string) *pooledEntry {
	var toClose []*mcpclient.Client

	m.mu.Lock()
	now := m.now()
	for key, e := range m.pool {
		if key != token && now.Sub(e.lastUsed) >= idleTTL {
			if e.client != nil {
				toClose = append(toClose, e.client)
			}
			delete(m.pool, key)
		}
	}
	if len(m.pool) >= maxPool {
		toClose = append(toClose, m.evictOldestLocked(token)...)
	}
	entry, ok := m.pool[token]
	if !ok {
		entry = &pooledEntry{}
		m.pool[token] = entry
	}
	entry.lastUsed = now
	m.mu.Unlock()

	// Close evicted sessions outside the pool lock — Close does I/O.
	for _, c := range toClose {
		_ = c.Close()
	}
	return entry
}

// evictOldestLocked drops the longest-idle entry (never the current key) to
// hold the size cap. Caller holds m.mu.
func (m *mcpCaller) evictOldestLocked(current string) []*mcpclient.Client {
	var oldestKey string
	var oldest time.Time
	for key, e := range m.pool {
		if key == current {
			continue
		}
		if oldestKey == "" || e.lastUsed.Before(oldest) {
			oldestKey, oldest = key, e.lastUsed
		}
	}
	if oldestKey == "" {
		return nil
	}
	e := m.pool[oldestKey]
	delete(m.pool, oldestKey)
	if e.client != nil {
		return []*mcpclient.Client{e.client}
	}
	return nil
}

// clientOf returns the entry's live MCP session, building and initializing
// one under the entry lock when absent.
func (m *mcpCaller) clientOf(ctx context.Context, entry *pooledEntry, token string) (*mcpclient.Client, error) {
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.client != nil {
		return entry.client, nil
	}

	opts := []transport.StreamableHTTPCOption{
		transport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer " + token}),
	}
	if m.http != nil {
		opts = append(opts, transport.WithHTTPBasicClient(m.http))
	}
	c, err := mcpclient.NewStreamableHttpClient(m.mcpURL, opts...)
	if err != nil {
		return nil, fmt.Errorf("build mcp client: %w", err)
	}
	if err := c.Start(ctx); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("start mcp client: %w", err)
	}
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "demarkus-library", Version: "0.1.0"}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		_ = c.Close()
		return nil, err
	}
	entry.client = c
	return c, nil
}

// invalidate retires a failed session so the next call rebuilds. The pointer
// compare makes it idempotent under races: only the goroutine that observes
// the still-current failed client nils and closes it; latecomers see a nil
// or already-rebuilt client and do nothing.
func (m *mcpCaller) invalidate(entry *pooledEntry, failed *mcpclient.Client) {
	entry.mu.Lock()
	current := entry.client == failed
	if current {
		entry.client = nil
	}
	entry.mu.Unlock()
	if current {
		_ = failed.Close()
	}
}

// close shuts down every pooled session. Called from Gateway.Close at
// process shutdown.
func (m *mcpCaller) close() {
	m.mu.Lock()
	entries := make([]*pooledEntry, 0, len(m.pool))
	for _, e := range m.pool {
		entries = append(entries, e)
	}
	m.pool = make(map[string]*pooledEntry)
	m.mu.Unlock()

	for _, e := range entries {
		e.mu.Lock()
		if e.client != nil {
			_ = e.client.Close()
			e.client = nil
		}
		e.mu.Unlock()
	}
}
