// Package world is the outbound adapter that implements port.WorldGateway over
// the demarkus QUIC fetch client. It translates transport-level status into
// domain errors so the core never sees protocol details.
//
// Phase 0 reads one world directly over QUIC. Phase 1 adds a sibling adapter
// that speaks MCP to the broker, swapped in at the composition root — the core
// is unaffected.
package world

import (
	"errors"
	"net"
	"strconv"

	"github.com/latebit/demarkus-library/internal/core/domain"
	"github.com/latebit/demarkus-library/internal/core/port"
	"github.com/latebit/demarkus/client/fetch"
	"github.com/latebit/demarkus/protocol"
)

// fetchClient is the slice of the demarkus client this adapter needs. An
// interface keeps the adapter unit-testable without a live world.
type fetchClient interface {
	Fetch(host, path, token string) (fetch.Result, error)
	List(host, path, token string) (fetch.Result, error)
	Versions(host, path, token string) (fetch.Result, error)
	Lookup(host, scope, query, token string, opts fetch.LookupOptions) (fetch.Result, error)
}

// Gateway adapts a demarkus world (one host) to the WorldGateway port.
type Gateway struct {
	client fetchClient
	host   string
	token  string
}

// compile-time check that Gateway satisfies the outbound port.
var _ port.WorldGateway = (*Gateway)(nil)

// NewGateway binds a fetch client to a world host and read token. The host is
// normalized to host:port because the fetch client dials it verbatim.
func NewGateway(client fetchClient, host, token string) *Gateway {
	return &Gateway{client: client, host: withDefaultPort(host), token: token}
}

// Fetch reads a document and maps demarkus status to domain errors.
func (g *Gateway) Fetch(path string) (domain.RawDocument, error) {
	res, err := g.client.Fetch(g.host, path, g.token)
	return g.toRawDocument(res, path, err)
}

// List reads a directory listing (the stacks) at path.
func (g *Gateway) List(path string) (domain.RawDocument, error) {
	res, err := g.client.List(g.host, path, g.token)
	return g.toRawDocument(res, path, err)
}

// Versions reads the edition history of the document at path.
func (g *Gateway) Versions(path string) (domain.RawDocument, error) {
	res, err := g.client.Versions(g.host, path, g.token)
	return g.toRawDocument(res, path, err)
}

// Lookup queries the world's catalog for query under scope.
func (g *Gateway) Lookup(scope, query string) (domain.RawDocument, error) {
	res, err := g.client.Lookup(g.host, scope, query, g.token, fetch.LookupOptions{})
	return g.toRawDocument(res, scope, err)
}

// toRawDocument maps a fetch result + transport error into a domain RawDocument,
// translating demarkus status into domain errors. This is the single place
// protocol status crosses into the domain.
func (g *Gateway) toRawDocument(res fetch.Result, path string, err error) (domain.RawDocument, error) {
	if err != nil {
		return domain.RawDocument{}, err
	}

	switch res.Response.Status {
	case protocol.StatusOK:
		// fall through
	case protocol.StatusNotFound, protocol.StatusArchived:
		return domain.RawDocument{}, domain.ErrNotFound
	case protocol.StatusUnauthorized, protocol.StatusNotPermitted:
		return domain.RawDocument{}, domain.ErrUnauthorized
	default:
		return domain.RawDocument{}, errors.New("world returned status: " + res.Response.Status)
	}

	return domain.RawDocument{
		Source:   g.host,
		Path:     path,
		Body:     res.Response.Body,
		Metadata: res.Response.Metadata,
	}, nil
}

// withDefaultPort appends the protocol port when the host omits one. The fetch
// client dials host:port directly (only ParseMarkURL fills the default).
// net.SplitHostPort distinguishes a real host:port from a bare IPv6 literal
// (e.g. 2001:db8::1), which a naive ":" check would misclassify; net.JoinHostPort
// brackets IPv6 hosts correctly.
func withDefaultPort(host string) string {
	if host == "" {
		return host
	}
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host // already has a port
	}
	return net.JoinHostPort(host, strconv.Itoa(protocol.DefaultPort))
}
