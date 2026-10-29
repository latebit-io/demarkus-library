package cache

import (
	"fmt"
	"sync"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus-library/internal/core/port"
)

func key(name string) port.CacheKey { return port.CacheKey{Kind: "d", Name: name} }

func TestGetPutRoundTrip(t *testing.T) {
	m := NewMemory(4)
	if _, ok := m.Get(key("k")); ok {
		t.Fatal("hit on empty cache")
	}
	m.Put(key("k"), domain.Document{Title: "A"})
	doc, ok := m.Get(key("k"))
	if !ok || doc.Title != "A" {
		t.Fatalf("got (%v, %v)", doc, ok)
	}
	// Put on an existing key refreshes the value.
	m.Put(key("k"), domain.Document{Title: "B"})
	if doc, _ := m.Get(key("k")); doc.Title != "B" {
		t.Errorf("refresh lost: %q", doc.Title)
	}
}

func TestEvictsLeastRecentlyUsed(t *testing.T) {
	m := NewMemory(2)
	m.Put(key("a"), domain.Document{Title: "a"})
	m.Put(key("b"), domain.Document{Title: "b"})
	m.Get(key("a"))                              // a is now more recent than b
	m.Put(key("c"), domain.Document{Title: "c"}) // evicts b
	if _, ok := m.Get(key("b")); ok {
		t.Error("b should have been evicted")
	}
	for _, k := range []string{"a", "c"} {
		if _, ok := m.Get(key(k)); !ok {
			t.Errorf("%s should have survived", k)
		}
	}
}

// Evict drops only matched keys, and the LRU stays consistent after: a later
// Put past capacity still evicts the true least-recent survivor.
func TestEvictDropsMatchedKeys(t *testing.T) {
	m := NewMemory(3)
	for _, k := range []string{"a1", "b1", "a2"} {
		m.Put(key(k), domain.Document{Title: k})
	}
	m.Evict(func(k port.CacheKey) bool { return k.Name[0] == 'a' })
	for _, k := range []string{"a1", "a2"} {
		if _, ok := m.Get(key(k)); ok {
			t.Errorf("%s survived Evict", k)
		}
	}
	m.Put(key("c1"), domain.Document{Title: "c1"})
	m.Put(key("c2"), domain.Document{Title: "c2"})
	m.Put(key("c3"), domain.Document{Title: "c3"}) // over capacity: b1 is oldest
	if _, ok := m.Get(key("b1")); ok {
		t.Error("b1 should be the LRU victim after Evict")
	}
	for _, k := range []string{"c1", "c2", "c3"} {
		if _, ok := m.Get(key(k)); !ok {
			t.Errorf("%s should have survived", k)
		}
	}
}

func TestConcurrentAccess(_ *testing.T) {
	m := NewMemory(16)
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			name := fmt.Sprintf("k%d", i%8)
			m.Put(key(name), domain.Document{Title: name})
			m.Get(key(name))
		})
	}
	wg.Wait()
}
