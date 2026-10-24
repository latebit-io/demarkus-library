package service

import (
	"context"
	"sync"
	"time"

	"github.com/latebit-io/demarkus-library/internal/core/port"
)

// universe is the scope of a value that spans every world (the floor, the hub).
const universe = ""

// ttlCache holds one timestamped value per scope and reader: the focused path
// reads live and puts, unfocused panes read through getFresh. Backs the floor,
// world map, name index and hub.
type ttlCache[T any] struct {
	mu     sync.Mutex
	all    map[string]map[string]ttlEntry[T] // scope → reader → entry
	epochs map[string]uint64                 // invalidations per scope, so a stale read cannot land
	retain time.Duration                     // entries older than this are swept; 0 keeps them until invalidated
	swept  time.Time                         // last sweep: put sweeps at most once per retain
	now    func() time.Time                  // nil ⇒ time.Now (overridable in tests)
}

type ttlEntry[T any] struct {
	value    T
	storedAt time.Time
}

func (c *ttlCache[T]) clockNow() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// getFresh returns the reader's cached value for scope only when it is
// younger than ttl. The reader comes from ctx, so no caller can leave it out.
func (c *ttlCache[T]) getFresh(ctx context.Context, scope string, ttl time.Duration) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.all[scope][port.ReaderKey(ctx)]
	if !ok || c.clockNow().Sub(e.storedAt) >= ttl {
		var zero T
		return zero, false
	}
	return e.value, true
}

// epoch is taken before a live read and handed to put: a write that
// invalidates the scope while the read is in flight makes put drop the result.
func (c *ttlCache[T]) epoch(scope string) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.epochs[scope]
}

// put stores the reader's value for scope unless the scope was invalidated
// since epoch was taken. Readers are an unbounded set, so put also sweeps
// entries past retain, at most once per retain window.
func (c *ttlCache[T]) put(ctx context.Context, scope string, epoch uint64, value T) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.epochs[scope] != epoch {
		return
	}
	now := c.clockNow()
	if c.retain > 0 && now.Sub(c.swept) >= c.retain {
		c.sweep(now)
		c.swept = now
	}
	if c.all == nil {
		c.all = make(map[string]map[string]ttlEntry[T])
	}
	readers := c.all[scope]
	if readers == nil {
		readers = make(map[string]ttlEntry[T])
		c.all[scope] = readers
	}
	readers[port.ReaderKey(ctx)] = ttlEntry[T]{value: value, storedAt: now}
}

// sweep drops entries older than retain, and scopes left empty. Caller holds mu.
func (c *ttlCache[T]) sweep(now time.Time) {
	for scope, readers := range c.all {
		for reader, e := range readers {
			if now.Sub(e.storedAt) >= c.retain {
				delete(readers, reader)
			}
		}
		if len(readers) == 0 {
			delete(c.all, scope)
		}
	}
}

// invalidate drops scope for every reader so the next read rebuilds it, called
// after a write so a just-published document shows at once.
func (c *ttlCache[T]) invalidate(scope string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.epochs == nil {
		c.epochs = make(map[string]uint64)
	}
	c.epochs[scope]++
	delete(c.all, scope)
}
