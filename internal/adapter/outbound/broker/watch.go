package broker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/latebit-io/demarkus-library/internal/adapter/bearer"
	"github.com/latebit-io/demarkus/client/mcpwatch"
)

// The pool lends signed-in readers' sessions to the pod's world watches (ADR
// 0047 decision 10): a knowledge world's feed is the same for every org
// member, so one subscription per world rides on whichever bearer lasts longest.

// lendMinLife is how much life a bearer needs left to be lent: less, and the
// watch would renew almost at once.
const lendMinLife = 30 * time.Second

var (
	errNoReader  = fmt.Errorf("broker: lend a session: %w", bearer.ErrNoReader)
	errNoSession = errors.New("broker: watches need the MCP session pool")
)

// lender is a pool entry whose bearer a watch may borrow.
type lender struct {
	token   string
	entry   *pooledEntry
	expires time.Time
}

// WatchSource is an mcpwatch.Source: the session of the signed-in reader whose
// bearer lapses last. With no such reader it fails with bearer.ErrNoReader.
func (g *Gateway) WatchSource(ctx context.Context) (mcpwatch.Session, error) {
	m, ok := g.caller.(*mcpCaller)
	if !ok {
		return mcpwatch.Session{}, errNoSession
	}
	l, ok := m.freshest()
	if !ok {
		return mcpwatch.Session{}, errNoReader
	}
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()
	conn, err := m.connOf(ctx, l.entry, l.token)
	if err != nil {
		return mcpwatch.Session{}, fmt.Errorf("broker: watch session: %w", err)
	}
	return mcpwatch.Session{Listener: conn.listener, Expires: l.expires}, nil
}

// ReaderWorlds names the worlds visible to the reader WatchSource would lend:
// the pod has no world list of its own.
func (g *Gateway) ReaderWorlds(ctx context.Context) ([]string, error) {
	m, ok := g.caller.(*mcpCaller)
	if !ok {
		return nil, errNoSession
	}
	l, ok := m.freshest()
	if !ok {
		return nil, errNoReader
	}
	worlds, err := g.Worlds(bearer.WithExpiry(bearer.WithToken(ctx, l.token), l.expires))
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(worlds))
	for _, world := range worlds {
		names = append(names, world.Name)
	}
	return names, nil
}

// freshest picks the entry whose bearer lapses last. An entry of unknown
// expiry is never lent: nothing says it will outlive the watch's first renewal.
func (m *mcpCaller) freshest() (lender, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var best lender
	for token, entry := range m.pool {
		if entry.expires.After(best.expires) {
			best = lender{token: token, entry: entry, expires: entry.expires}
		}
	}
	if best.entry == nil || best.expires.Sub(m.now()) < lendMinLife {
		return lender{}, false
	}
	return best, true
}
