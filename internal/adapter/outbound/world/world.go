// Package world is the outbound adapter that implements port.WorldGateway over
// the demarkus QUIC fetch client. It translates transport-level status into
// domain errors so the core never sees protocol details.
//
// The world identifier on this adapter is a demarkus host[:port], dialed
// directly. A configured read token is attached ONLY when dialing the home
// host — following a link into the distributed graph must never leak the home
// world's credential to whatever server the link points at.
package world

import (
	"context"
	"errors"
	"net"
	"strconv"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus-library/internal/core/port"
	"github.com/latebit-io/demarkus/client/fetch"
	"github.com/latebit-io/demarkus/protocol"
)

// fetchClient is the slice of the demarkus client this adapter needs;
// *fetch.Client satisfies it. An interface keeps the adapter unit-testable
// without a live world.
type fetchClient interface {
	Fetch(ctx context.Context, r fetch.FetchRequest) (fetch.Result, error)
	List(ctx context.Context, r fetch.ListRequest) (fetch.Result, error)
	Versions(ctx context.Context, r fetch.VersionsRequest) (fetch.Result, error)
	Lookup(ctx context.Context, r fetch.LookupRequest) (fetch.Result, error)
}

// Gateway adapts demarkus worlds (addressed by host) to the WorldGateway port.
type Gateway struct {
	client fetchClient
	home   string // normalized host:port whose reads carry the token; "" = none
	token  string
}

// compile-time check that Gateway satisfies the outbound port.
var _ port.WorldGateway = (*Gateway)(nil)

// NewGateway binds a fetch client to an optional home host and its read
// token. home may be empty (federation duty in broker mode): every dial is
// then tokenless.
func NewGateway(client fetchClient, home, token string) *Gateway {
	return &Gateway{client: client, home: NormalizeHost(home), token: token}
}

// Fetch reads a document and maps demarkus status to domain errors.
func (g *Gateway) Fetch(ctx context.Context, world, path string) (domain.RawDocument, error) {
	host := NormalizeHost(world)
	res, err := g.client.Fetch(ctx, fetch.FetchRequest{Host: host, Path: path, Token: g.tokenFor(host)})
	return g.toRawDocument(res, host, path, err)
}

// List reads a directory listing (the stacks) at path.
func (g *Gateway) List(ctx context.Context, world, path string) (domain.RawDocument, error) {
	host := NormalizeHost(world)
	res, err := g.client.List(ctx, fetch.ListRequest{Host: host, Path: path, Token: g.tokenFor(host)})
	return g.toRawDocument(res, host, path, err)
}

// Versions reads the edition history of the document at path.
func (g *Gateway) Versions(ctx context.Context, world, path string) (domain.RawDocument, error) {
	host := NormalizeHost(world)
	res, err := g.client.Versions(ctx, fetch.VersionsRequest{Host: host, Path: path, Token: g.tokenFor(host)})
	return g.toRawDocument(res, host, path, err)
}

// Lookup queries the world's catalog for req.Query under req.Scope, optionally
// narrowed by a comma-separated key=value filter (tag pages use tag=<tag>).
// A server without body match answers q.Match=body from the catalog and omits
// the match echo, which callers read as catalog fallback.
func (g *Gateway) Lookup(ctx context.Context, world string, q domain.LookupQuery) (domain.RawDocument, error) {
	host := NormalizeHost(world)
	res, err := g.client.Lookup(ctx, fetch.LookupRequest{
		Host:   host,
		Scope:  q.Scope,
		Query:  q.Query,
		Token:  g.tokenFor(host),
		Filter: q.Filter,
		Limit:  q.Limit,
		Match:  q.Match,
	})
	return g.toRawDocument(res, host, q.Scope, err)
}

// LookupAll is the direct gateway's one-world universe.
func (g *Gateway) LookupAll(ctx context.Context, q domain.LookupQuery) (domain.RawDocument, error) {
	if g.home == "" {
		return domain.RawDocument{}, domain.ErrNotFound
	}
	return g.Lookup(ctx, g.home, q)
}

// Worlds returns the single-world universe: the home world, when this
// gateway has one. A homeless gateway (broker-mode federation duty) has no
// universe of its own — empty, not an error. The non-brokered universe is
// extensional (the home world plus whatever it links to); enumeration
// beyond home is discovery's job, not this adapter's.
func (g *Gateway) Worlds(_ context.Context) ([]domain.WorldInfo, error) {
	if g.home == "" {
		return nil, nil
	}
	return []domain.WorldInfo{{Name: g.home, URL: "mark://" + g.home}}, nil
}

// Publish has no path on this adapter: the library's QUIC fetch client is
// read-only (no write token wired), so writes degrade honestly rather than
// silently failing (Phase 3; broker-mode worlds write through mark_publish).
func (g *Gateway) Publish(_ context.Context, _ domain.PublishRequest) (domain.PublishResult, error) {
	return domain.PublishResult{}, domain.ErrWriteUnsupported
}

// Append is unsupported on the read-only QUIC client, like Publish.
func (g *Gateway) Append(_ context.Context, _, _, _ string) (int, error) {
	return 0, domain.ErrWriteUnsupported
}

// tokenFor scopes the read token to the home host. Any other host in the
// distributed graph gets an anonymous read — public documents only.
func (g *Gateway) tokenFor(host string) string {
	if g.home != "" && host == g.home {
		return g.token
	}
	return ""
}

// toRawDocument maps a fetch result + transport error into a domain RawDocument,
// translating demarkus status into domain errors. This is the single place
// protocol status crosses into the domain.
func (g *Gateway) toRawDocument(res fetch.Result, host, path string, err error) (domain.RawDocument, error) {
	if err != nil {
		return domain.RawDocument{}, err
	}

	switch res.Response.Status {
	case protocol.StatusOK:
		// fall through
	case protocol.StatusNotFound:
		return domain.RawDocument{}, domain.ErrNotFound
	case protocol.StatusArchived:
		return domain.RawDocument{}, domain.ErrArchived
	case protocol.StatusUnauthorized, protocol.StatusNotPermitted:
		return domain.RawDocument{}, domain.ErrUnauthorized
	default:
		return domain.RawDocument{}, errors.New("world returned status: " + res.Response.Status)
	}

	return domain.RawDocument{
		Source:   host,
		Path:     path,
		Body:     res.Response.Body,
		Metadata: res.Response.Metadata,
	}, nil
}

// NormalizeHost appends the protocol port when the host omits one. The fetch
// client dials host:port verbatim (only ParseMarkURL fills the default), and
// token scoping compares normalized forms. net.SplitHostPort distinguishes a
// real host:port from a bare IPv6 literal (e.g. 2001:db8::1), which a naive
// ":" check would misclassify; net.JoinHostPort brackets IPv6 hosts correctly.
func NormalizeHost(host string) string {
	if host == "" {
		return host
	}
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host // already has a port
	}
	return net.JoinHostPort(host, strconv.Itoa(protocol.DefaultPort))
}
