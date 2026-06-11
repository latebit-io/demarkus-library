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
	"strconv"
	"strings"

	"github.com/latebit/demarkus-library/internal/core/domain"
	"github.com/latebit/demarkus-library/internal/core/port"
	"github.com/latebit/demarkus/client/fetch"
	"github.com/latebit/demarkus/protocol"
)

// fetchClient is the slice of the demarkus client this adapter needs. An
// interface keeps the adapter unit-testable without a live world.
type fetchClient interface {
	Fetch(host, path, token string) (fetch.Result, error)
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
func withDefaultPort(host string) string {
	if host == "" || strings.Contains(host, ":") {
		return host
	}
	return host + ":" + strconv.Itoa(protocol.DefaultPort)
}
