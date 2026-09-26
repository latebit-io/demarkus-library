package service

import (
	"sync"
	"time"
)

// ttlCache holds one value per key, each timestamped so a read can insist it
// is younger than a TTL. The focused path reads live and puts; unfocused
// panes read through getFresh. Shared by the world map and the name index.
type ttlCache[T any] struct {
	mu     sync.Mutex
	all    map[string]ttlEntry[T]
	epochs map[string]uint64 // invalidations per key, so a stale read cannot land
	now    func() time.Time  // nil ⇒ time.Now (overridable in tests)
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

// getFresh returns the cached value for key only when it is younger than ttl.
func (c *ttlCache[T]) getFresh(key string, ttl time.Duration) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.all[key]
	if !ok || c.clockNow().Sub(e.storedAt) >= ttl {
		var zero T
		return zero, false
	}
	return e.value, true
}

// epoch is taken before a live read and handed to put: a write that
// invalidates the key while the read is in flight makes put drop the result.
func (c *ttlCache[T]) epoch(key string) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.epochs[key]
}

// put stores value unless key was invalidated since epoch was taken.
func (c *ttlCache[T]) put(key string, epoch uint64, value T) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.epochs[key] != epoch {
		return
	}
	if c.all == nil {
		c.all = make(map[string]ttlEntry[T])
	}
	c.all[key] = ttlEntry[T]{value: value, storedAt: c.clockNow()}
}

// invalidate drops a key so the next read rebuilds it, called after a write so
// a just-published document shows at once.
func (c *ttlCache[T]) invalidate(key string) {
	c.mu.Lock()
	if c.epochs == nil {
		c.epochs = make(map[string]uint64)
	}
	c.epochs[key]++
	delete(c.all, key)
	c.mu.Unlock()
}
