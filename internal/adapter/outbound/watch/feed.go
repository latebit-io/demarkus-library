// Package watch is the outbound adapter that implements port.ChangeFeed over
// demarkus WATCH: SPEC §6.8 over QUIC, or §6.8.1 over the broker's MCP
// binding. One subscription per watched world per pod, shared in process,
// never one per browser (plans/watch-realtime.md).
package watch

import (
	"context"
	"log/slog"
	"time"

	"github.com/latebit-io/demarkus-library/internal/adapter/outbound/world"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus-library/internal/core/port"
	"github.com/latebit-io/demarkus/client/fetch"
	"github.com/latebit-io/demarkus/client/mcpwatch"
	"github.com/latebit-io/demarkus/client/memory/graphstore"
	"github.com/latebit-io/demarkus/protocol"
)

// subscriber opens a watch; *fetch.Client satisfies it.
type subscriber interface {
	Watch(ctx context.Context, r fetch.WatchRequest) (*fetch.Watch, error)
}

// noticeSource is the slice of *fetch.Watch the feed reads.
type noticeSource interface {
	Next(ctx context.Context) (fetch.Notice, error)
	Cursor() protocol.Cursor
	Close()
}

// Backoff between subscriptions. fetch.Watch already rides out dropped streams
// and stalls; the feed only reopens a watch the server ended for good.
const (
	retryMin = time.Second
	retryMax = time.Minute
)

// Feed watches every path of one world. Implements port.ChangeFeed.
type Feed struct {
	open     func(ctx context.Context, since protocol.Cursor) (noticeSource, error)
	world    string // stamped on every change: a normalized host or a world name
	logger   *slog.Logger
	retryMin time.Duration
	retryMax time.Duration
	roomPoll time.Duration
}

// compile-time check that Feed satisfies the outbound port.
var _ port.ChangeFeed = (*Feed)(nil)

// NewFeed watches host with token, which is empty for a public world.
func NewFeed(client subscriber, host, token string, logger *slog.Logger) *Feed {
	host = world.NormalizeHost(host)
	return &Feed{
		open: func(ctx context.Context, since protocol.Cursor) (noticeSource, error) {
			w, err := client.Watch(ctx, fetch.WatchRequest{Host: host, Path: "/", Token: token, Since: since, Coalesce: true})
			if err != nil {
				return nil, err
			}
			return w, nil
		},
		world: host, logger: logger, retryMin: retryMin, retryMax: retryMax, roomPoll: roomPoll,
	}
}

// NewMCPFeed watches the world named name over the broker's MCP binding, on
// the sessions source lends.
func NewMCPFeed(source mcpwatch.Source, name string, logger *slog.Logger) *Feed {
	uri := mcpwatch.SubscriptionURI(name, "/")
	return &Feed{
		open: func(ctx context.Context, since protocol.Cursor) (noticeSource, error) {
			w, err := mcpwatch.Open(ctx, source, mcpwatch.Request{URI: uri, Since: since, Coalesce: true})
			if err != nil {
				return nil, err
			}
			return w, nil
		},
		world: name, logger: logger, retryMin: retryMin, retryMax: retryMax, roomPoll: roomPoll,
	}
}

// Changes starts the watch and returns its hints; the channel closes when ctx ends.
func (f *Feed) Changes(ctx context.Context) <-chan domain.Change {
	out := make(chan domain.Change)
	go func() {
		defer close(out)
		f.run(ctx, out)
	}()
	return out
}

// run keeps one watch open until ctx ends. Each reopen resumes from the last
// cursor read, and the server answers resync when that cursor is gone, so no
// change goes unreported.
func (f *Feed) run(ctx context.Context, out chan<- domain.Change) {
	var since protocol.Cursor
	pace := newPacer(f.retryMin, f.retryMax, f.roomPoll)
	for {
		watch, err := f.open(ctx, since)
		if err == nil {
			started := time.Now()
			f.logger.Info("watch: subscribed", "world", f.world, "resumed", !since.IsZero())
			err = f.follow(ctx, watch, out)
			since = watch.Cursor()
			watch.Close()
			if time.Since(started) >= f.retryMax {
				pace.reset() // a long-lived watch earns a fresh backoff
			}
		}
		if ctx.Err() != nil {
			return
		}
		wait, report := pace.next(err)
		if report {
			f.logger.Log(ctx, retryLevel(err), "watch: retrying", "world", f.world, "retry_in", wait, "err", err)
		}
		if !sleep(ctx, wait) {
			return
		}
	}
}

// follow forwards one watch's notices until it ends or ctx does.
func (f *Feed) follow(ctx context.Context, watch noticeSource, out chan<- domain.Change) error {
	for {
		notice, err := watch.Next(ctx)
		if err != nil {
			return err
		}
		change, ok := f.change(notice)
		if !ok {
			continue
		}
		select {
		case out <- change:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// ops maps the protocol's event ops onto the domain's.
var ops = map[string]string{
	protocol.OpPublish: domain.OpPublish,
	protocol.OpAppend:  domain.OpAppend,
	protocol.OpArchive: domain.OpArchive,
}

// change maps a notice to a hint; ok is false for an op this client does not
// know, which the protocol says to skip rather than fail on.
func (f *Feed) change(notice fetch.Notice) (domain.Change, bool) {
	if notice.Resync {
		return domain.Change{World: f.world, Resync: true}, true
	}
	event := notice.Event
	op, known := ops[event.Op]
	if !known {
		return domain.Change{}, false
	}
	return domain.Change{World: f.world, Path: event.Path, Op: op, Version: event.Version,
		Agent: event.Agent, User: event.User, Derived: graphstore.IsGeneratedGraphPath(event.Path)}, true
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
