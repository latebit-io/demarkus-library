package service

import (
	"context"
	"sync"
	"time"

	"github.com/latebit-io/demarkus-library/internal/core/port"
)

// universe is the scope of a value that spans every world (the floor, the hub).
const universe = ""

// ttlCache holds one value per scope and reader, each timestamped so a read
// can insist it is younger than a TTL. The focused path reads live and puts;
// unfocused panes read through getFresh. Backs the floor, world map, name
// index and hub.
type ttlCache[T any] struct {
	mu     sync.Mutex
	all    map[cacheKey]ttlEntry[T]
	epochs map[string]uint64 // invalidations per scope, so a stale read cannot land
	retain time.Duration     // put drops entries older than this; 0 keeps them until invalidated
	now    func() time.Time  // nil ⇒ time.Now (overridable in tests)
}

// cacheKey names one cached value: what it describes and whose permission
// mask built it. Readers see different worlds, so one reader's value must
// never serve another; the key is built here so no caller can leave it out.
type cacheKey struct {
	scope  string
	reader string
}

func keyFor(ctx context.Context, scope string) cacheKey {
	return cacheKey{scope: scope, reader: port.ReaderKey(ctx)}
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
// younger than ttl.
func (c *ttlCache[T]) getFresh(ctx context.Context, scope string, ttl time.Duration) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.all[keyFor(ctx, scope)]
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
// since epoch was taken. Keys carry the reader, an unbounded set, so put also
// drops every entry past retain.
func (c *ttlCache[T]) put(ctx context.Context, scope string, epoch uint64, value T) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.epochs[scope] != epoch {
		return
	}
	now := c.clockNow()
	if c.all == nil {
		c.all = make(map[cacheKey]ttlEntry[T])
	}
	if c.retain > 0 {
		for key, e := range c.all {
			if now.Sub(e.storedAt) >= c.retain {
				delete(c.all, key)
			}
		}
	}
	c.all[keyFor(ctx, scope)] = ttlEntry[T]{value: value, storedAt: now}
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
	for key := range c.all {
		if key.scope == scope {
			delete(c.all, key)
		}
	}
}
