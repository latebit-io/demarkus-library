// Package broker is the outbound adapter that implements port.WorldGateway
// over the broker's MCP gateway (/mcp, Streamable HTTP) — the Phase 1b
// transport (ADR 0004). Where the world adapter dials a single world over
// QUIC with a static token, this one presents the logged-in reader's bearer
// (from the request context via the bearer package) and reads through the
// broker's mark_* tools.
//
// Connection model: stateless per-call. Every read builds a fresh MCP client,
// runs the initialize handshake, calls one tool, and closes. The bearer is
// per-reader and the broker re-authenticates every HTTP request anyway, so a
// long-lived shared MCP session would couple readers to one identity for no
// gain. Cost: one extra round trip per read (initialize). If that ever shows
// up in latency, the upgrade path is a per-session client pool keyed by
// session id — tracked as debt, not built speculatively.
package broker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/latebit/demarkus-library/internal/adapter/bearer"
	"github.com/latebit/demarkus-library/internal/core/domain"
	"github.com/latebit/demarkus-library/internal/core/port"
	"github.com/latebit/demarkus/protocol"
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

// Gateway adapts the broker MCP gateway to the WorldGateway port for one
// world. The reader's bearer rides in on the request context.
type Gateway struct {
	caller toolCaller
	world  string
}

// compile-time check that Gateway satisfies the outbound port.
var _ port.WorldGateway = (*Gateway)(nil)

// NewGateway builds the production gateway: brokerURL is the broker origin
// (https://broker.example.org), world the demarkus world name documents are
// read from. A nil httpClient gets a 15-second-timeout default — safe to
// bound at the client level because the stateless-per-call model never holds
// a long-lived SSE stream open; without it a wedged broker would pin request
// handlers indefinitely.
func NewGateway(brokerURL, world string, httpClient *http.Client) *Gateway {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Gateway{
		caller: &mcpCaller{
			mcpURL: strings.TrimRight(brokerURL, "/") + "/mcp",
			http:   httpClient,
		},
		world: world,
	}
}

// Fetch reads a document through mark_fetch.
func (g *Gateway) Fetch(ctx context.Context, path string) (domain.RawDocument, error) {
	return g.read(ctx, "mark_fetch", path, map[string]any{"url": g.markURL(path)})
}

// List reads a directory listing (the stacks) through mark_list.
func (g *Gateway) List(ctx context.Context, path string) (domain.RawDocument, error) {
	return g.read(ctx, "mark_list", path, map[string]any{"url": g.markURL(path)})
}

// Versions reads the edition history through mark_versions.
func (g *Gateway) Versions(ctx context.Context, path string) (domain.RawDocument, error) {
	return g.read(ctx, "mark_versions", path, map[string]any{"url": g.markURL(path)})
}

// Lookup queries the catalog under scope through mark_lookup.
func (g *Gateway) Lookup(ctx context.Context, scope, query string) (domain.RawDocument, error) {
	return g.read(ctx, "mark_lookup", scope, map[string]any{
		"url":   g.markURL(scope),
		"query": query,
	})
}

// read runs one tool call and maps the outcome into the domain.
func (g *Gateway) read(ctx context.Context, tool, path string, args map[string]any) (domain.RawDocument, error) {
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
	return parseToolResult(g.world, path, text)
}

// markURL builds the mark://<world><path> tool argument. path always starts
// with / on this port.
func (g *Gateway) markURL(path string) string {
	return "mark://" + g.world + path
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
// per call (see the package comment for why stateless-per-call).
type mcpCaller struct {
	mcpURL string
	http   *http.Client
}

func (m *mcpCaller) callTool(ctx context.Context, token, tool string, args map[string]any) (string, bool, error) {
	opts := []transport.StreamableHTTPCOption{
		transport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer " + token}),
	}
	if m.http != nil {
		opts = append(opts, transport.WithHTTPBasicClient(m.http))
	}
	c, err := mcpclient.NewStreamableHttpClient(m.mcpURL, opts...)
	if err != nil {
		return "", false, fmt.Errorf("build mcp client: %w", err)
	}
	defer c.Close()

	if err := c.Start(ctx); err != nil {
		return "", false, fmt.Errorf("start mcp client: %w", err)
	}
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "demarkus-library", Version: "0.1.0"}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		return "", false, fmt.Errorf("initialize: %w", err)
	}

	callReq := mcp.CallToolRequest{}
	callReq.Params.Name = tool
	callReq.Params.Arguments = args
	res, err := c.CallTool(ctx, callReq)
	if err != nil {
		return "", false, fmt.Errorf("call %s: %w", tool, err)
	}

	var sb strings.Builder
	for _, content := range res.Content {
		if tc, ok := content.(mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String(), res.IsError, nil
}
