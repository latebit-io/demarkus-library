// Package cache is the outbound adapter that implements port.DocumentCache
// in memory: the rendered-document cache the trail engine requires (ADR 0005
// decision 9). A trail render reads every unfocused pane from here, so a
// click costs one world read instead of N.
//
// Plain LRU, no TTL: the focused pane is always re-read, and change hints
// evict what a write made stale, so age alone never matters. Per-pod: a
// replica that misses re-reads the world, so no sharing needed.
package cache

import (
	"container/list"
	"sync"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus-library/internal/core/port"
)

// Memory is a fixed-capacity LRU over rendered documents.
type Memory struct {
	mu      sync.Mutex
	cap     int
	order   *list.List                      // front = most recently used
	entries map[port.CacheKey]*list.Element // key → element whose Value is *entry
}

type entry struct {
	key port.CacheKey
	doc domain.Document
}

// compile-time check that Memory satisfies the outbound port.
var _ port.DocumentCache = (*Memory)(nil)

// DefaultCapacity comfortably covers many concurrent 10-pane trails; at a
// few tens of KB of rendered HTML per document this stays in the tens of MB.
const DefaultCapacity = 512

// NewMemory builds an LRU document cache. capacity <= 0 uses DefaultCapacity.
func NewMemory(capacity int) *Memory {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &Memory{
		cap:     capacity,
		order:   list.New(),
		entries: make(map[port.CacheKey]*list.Element, capacity),
	}
}

// Get returns the cached document for key, marking it most recently used.
func (m *Memory) Get(key port.CacheKey) (domain.Document, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	el, ok := m.entries[key]
	if !ok {
		return domain.Document{}, false
	}
	m.order.MoveToFront(el)
	return el.Value.(*entry).doc, true
}

// Put stores (or refreshes) the document for key, evicting the least
// recently used entry past capacity.
func (m *Memory) Put(key port.CacheKey, doc domain.Document) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if el, ok := m.entries[key]; ok {
		el.Value.(*entry).doc = doc
		m.order.MoveToFront(el)
		return
	}
	m.entries[key] = m.order.PushFront(&entry{key: key, doc: doc})
	if m.order.Len() > m.cap {
		oldest := m.order.Back()
		m.order.Remove(oldest)
		delete(m.entries, oldest.Value.(*entry).key)
	}
}

// Evict drops every entry whose key match reports true. A full scan: at most
// cap entries, and changes arrive far less often than reads.
func (m *Memory) Evict(match func(key port.CacheKey) bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, el := range m.entries {
		if match(key) {
			m.order.Remove(el)
			delete(m.entries, key)
		}
	}
}
