package service

import (
	"sync"
	"time"
)

// ttlCache holds one value per key, each timestamped so a read can insist it
// is younger than a TTL. The focused path reads live and puts; unfocused
// panes read through getFresh. Shared by the world map and the name index.
type ttlCache[T any] struct {
	mu  sync.Mutex
	all map[string]ttlEntry[T]
	now func() time.Time // nil ⇒ time.Now (overridable in tests)
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

func (c *ttlCache[T]) put(key string, value T) {
	c.mu.Lock()
	if c.all == nil {
		c.all = make(map[string]ttlEntry[T])
	}
	c.all[key] = ttlEntry[T]{value: value, storedAt: c.clockNow()}
	c.mu.Unlock()
}

// invalidate drops a key so the next read rebuilds it, called after a write so
// a just-published document shows at once.
func (c *ttlCache[T]) invalidate(key string) {
	c.mu.Lock()
	delete(c.all, key)
	c.mu.Unlock()
}
