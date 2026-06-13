// Package federated is the outbound composite that makes the distributed
// knowledge graph traversable: it routes each read to the right transport by
// the shape of the world identifier.
//
//   - A bare name ("root", "team-a") is a knowledge-system world, resolved by
//     the broker gateway.
//   - A host-shaped identifier ("soul.demarkus.io", "wiki.example.org:6309")
//     is a demarkus server anywhere on the network, dialed directly over the
//     QUIC gateway.
//
// Either side may be absent: quic-only deployments have no name resolver, and
// broker deployments may disable direct federation. A world that has no route
// reads as not-found — the link renders, the click 404s, nothing leaks.
package federated

import (
	"context"
	"strings"

	"github.com/latebit-io/demarkus-library/internal/adapter/outbound/world"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus-library/internal/core/port"
)

// Config wires the composite. Zero-value fields disable that route.
type Config struct {
	// Names resolves knowledge-system world names (the broker gateway).
	Names port.WorldGateway
	// Hosts dials demarkus servers directly (the QUIC gateway).
	Hosts port.WorldGateway
	// HomeHost, when set, is always dialable through Hosts even with
	// AllowExternal off — the quic-mode home world is not "external".
	HomeHost string
	// AllowExternal permits following links to arbitrary hosts on the
	// network (DEMARKUS_FEDERATION). Off = home and named worlds only.
	AllowExternal bool
}

// Gateway routes reads across transports. Implements port.WorldGateway.
type Gateway struct {
	cfg  Config
	home string // normalized HomeHost
}

var _ port.WorldGateway = (*Gateway)(nil)

// New builds the composite router.
func New(cfg Config) *Gateway {
	return &Gateway{cfg: cfg, home: world.NormalizeHost(cfg.HomeHost)}
}

// Fetch routes to the world's transport.
func (g *Gateway) Fetch(ctx context.Context, w, path string) (domain.RawDocument, error) {
	gw, err := g.route(w)
	if err != nil {
		return domain.RawDocument{}, err
	}
	return gw.Fetch(ctx, w, path)
}

// List routes to the world's transport.
func (g *Gateway) List(ctx context.Context, w, path string) (domain.RawDocument, error) {
	gw, err := g.route(w)
	if err != nil {
		return domain.RawDocument{}, err
	}
	return gw.List(ctx, w, path)
}

// Versions routes to the world's transport.
func (g *Gateway) Versions(ctx context.Context, w, path string) (domain.RawDocument, error) {
	gw, err := g.route(w)
	if err != nil {
		return domain.RawDocument{}, err
	}
	return gw.Versions(ctx, w, path)
}

// Lookup routes to the world's transport.
func (g *Gateway) Lookup(ctx context.Context, w, scope, query, filter string) (domain.RawDocument, error) {
	gw, err := g.route(w)
	if err != nil {
		return domain.RawDocument{}, err
	}
	return gw.Lookup(ctx, w, scope, query, filter)
}

// Worlds enumerates the universe: the broker's authorization-filtered list
// when a name resolver is wired, otherwise the QUIC side's home world.
func (g *Gateway) Worlds(ctx context.Context) ([]domain.WorldInfo, error) {
	if g.cfg.Names != nil {
		return g.cfg.Names.Worlds(ctx)
	}
	if g.cfg.Hosts != nil {
		return g.cfg.Hosts.Worlds(ctx)
	}
	return nil, nil
}

// route picks the transport for a world identifier. No route → ErrNotFound:
// to the reader an unroutable world is indistinguishable from a world with
// nothing at that path, and the error page already speaks that language.
func (g *Gateway) route(w string) (port.WorldGateway, error) {
	if !IsHostShaped(w) {
		if g.cfg.Names == nil {
			return nil, domain.ErrNotFound
		}
		return g.cfg.Names, nil
	}
	if g.cfg.Hosts == nil {
		return nil, domain.ErrNotFound
	}
	if !g.cfg.AllowExternal && (g.home == "" || world.NormalizeHost(w) != g.home) {
		return nil, domain.ErrNotFound
	}
	return g.cfg.Hosts, nil
}

// IsHostShaped reports whether a world identifier addresses a server directly
// (host or host:port) rather than naming a knowledge-system world. Dots and
// colons never appear in broker world names; they are how hosts spell
// themselves — plus bare "localhost", the one portless dotless host the
// stack accepts elsewhere. Exported because the web adapter applies the same
// test when rewriting mark:// links.
func IsHostShaped(w string) bool {
	return w == "localhost" || strings.ContainsAny(w, ".:")
}
