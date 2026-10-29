package broker

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/latebit-io/demarkus-library/internal/adapter/bearer"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus/client/mcpwatch"
)

// The pool lends signed-in readers' sessions to the pod's world watches (ADR
// 0047 decision 10): a knowledge world's feed is the same for every org
// member who sees it, so one subscription per world rides on one such bearer.

// lendMinLife is how much life a bearer needs left to be lent: less, and the
// watch would renew almost at once.
const lendMinLife = 30 * time.Second

var (
	errNoReader  = fmt.Errorf("broker: lend a session: %w", bearer.ErrNoReader)
	errNoSession = errors.New("broker: watches need the MCP session pool")
)

// lender is a pool entry whose bearer a watch may borrow, and the worlds its
// reader's last world list showed.
type lender struct {
	token   string
	entry   *pooledEntry
	expires time.Time
	worlds  []string
}

// as acts as the lender's reader.
func (l lender) as(ctx context.Context) context.Context {
	return bearer.WithExpiry(bearer.WithToken(ctx, l.token), l.expires)
}

// WorldSource is the mcpwatch.Source for world's feed: the session of the
// reader who sees world and whose bearer lapses last. With no such reader it
// fails with bearer.ErrNoReader.
func (g *Gateway) WorldSource(world string) mcpwatch.Source {
	return func(ctx context.Context) (mcpwatch.Session, error) {
		m, ok := g.caller.(*mcpCaller)
		if !ok {
			return mcpwatch.Session{}, errNoSession
		}
		l, ok := lenderFor(m.lenders(), world)
		if !ok {
			return mcpwatch.Session{}, errNoReader
		}
		ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
		conn, err := m.connOf(ctx, l.entry, l.token)
		if err != nil {
			return mcpwatch.Session{}, fmt.Errorf("broker: watch session for %s: %w", world, err)
		}
		return mcpwatch.Session{Listener: conn.listener, Expires: l.expires}, nil
	}
}

// ReaderWorlds names every world some signed-in reader sees: the pod has no
// world list of its own. Listing as each reader keeps what the lenders see,
// and so which of them WorldSource lends, current.
func (g *Gateway) ReaderWorlds(ctx context.Context) ([]string, error) {
	m, ok := g.caller.(*mcpCaller)
	if !ok {
		return nil, errNoSession
	}
	lenders := m.lenders()
	if len(lenders) == 0 {
		return nil, errNoReader
	}
	var failed []error
	for _, l := range lenders {
		if _, err := g.Worlds(l.as(ctx)); err != nil {
			failed = append(failed, err)
		}
	}
	// Every list failed: say so, so the feeds already running are kept.
	if len(failed) == len(lenders) {
		return nil, fmt.Errorf("broker: list worlds for %d readers: %w", len(lenders), failed[0])
	}
	return seenWorlds(m.lenders()), nil
}

// noteWorlds records which worlds the bearer's list showed, after a list, so
// WorldSource lends it only for those. A refused list forgets them.
func (g *Gateway) noteWorlds(token string, worlds []domain.WorldInfo, err error) {
	m, ok := g.caller.(*mcpCaller)
	if !ok || (err != nil && !errors.Is(err, domain.ErrUnauthorized)) {
		return
	}
	var names []string
	for _, world := range worlds {
		names = append(names, world.Name)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if entry, ok := m.pool[token]; ok {
		entry.worlds = names
	}
}

// lenders lists the entries a watch may borrow: an entry of unknown expiry is
// never lent, since nothing says it will outlive the watch's first renewal.
func (m *mcpCaller) lenders() []lender {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	var out []lender
	for token, entry := range m.pool {
		if entry.expires.Sub(now) >= lendMinLife {
			out = append(out, lender{token: token, entry: entry, expires: entry.expires, worlds: entry.worlds})
		}
	}
	return out
}

// lenderFor picks, of the lenders who see world, the one whose bearer lapses
// last.
func lenderFor(lenders []lender, world string) (lender, bool) {
	var best lender
	for _, l := range lenders {
		if slices.Contains(l.worlds, world) && l.expires.After(best.expires) {
			best = l
		}
	}
	return best, best.entry != nil
}

// seenWorlds is every world some lender sees, sorted.
func seenWorlds(lenders []lender) []string {
	lists := make([][]string, 0, len(lenders))
	for _, l := range lenders {
		lists = append(lists, l.worlds)
	}
	worlds := slices.Concat(lists...)
	slices.Sort(worlds)
	return slices.Compact(worlds)
}
