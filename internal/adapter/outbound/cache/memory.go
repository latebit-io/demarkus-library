// Package cache is the outbound adapter that implements port.DocumentCache
// in memory: the rendered-document cache the trail engine requires (ADR 0005
// decision 9). A trail render reads every unfocused pane from here, so a
// click costs one world read instead of N.
//
// Plain LRU, no TTL: freshness is the service's focused-live policy (the
// pane being read is always fetched and re-cached), so age alone never
// matters — an unfocused pane shows at worst what the reader already saw.
// Per-pod, like the session store; the chart's replicaCount 1 + Recreate
// posture already assumes per-pod state.
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
	order   *list.List               // front = most recently used
	entries map[string]*list.Element // key → element whose Value is *entry
}

type entry struct {
	key string
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
		entries: make(map[string]*list.Element, capacity),
	}
}

// Get returns the cached document for key, marking it most recently used.
func (m *Memory) Get(key string) (domain.Document, bool) {
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
func (m *Memory) Put(key string, doc domain.Document) {
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
