package cache

import (
	"fmt"
	"sync"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

func TestGetPutRoundTrip(t *testing.T) {
	m := NewMemory(4)
	if _, ok := m.Get("k"); ok {
		t.Fatal("hit on empty cache")
	}
	m.Put("k", domain.Document{Title: "A"})
	doc, ok := m.Get("k")
	if !ok || doc.Title != "A" {
		t.Fatalf("got (%v, %v)", doc, ok)
	}
	// Put on an existing key refreshes the value.
	m.Put("k", domain.Document{Title: "B"})
	if doc, _ := m.Get("k"); doc.Title != "B" {
		t.Errorf("refresh lost: %q", doc.Title)
	}
}

func TestEvictsLeastRecentlyUsed(t *testing.T) {
	m := NewMemory(2)
	m.Put("a", domain.Document{Title: "a"})
	m.Put("b", domain.Document{Title: "b"})
	m.Get("a")                              // a is now more recent than b
	m.Put("c", domain.Document{Title: "c"}) // evicts b
	if _, ok := m.Get("b"); ok {
		t.Error("b should have been evicted")
	}
	for _, k := range []string{"a", "c"} {
		if _, ok := m.Get(k); !ok {
			t.Errorf("%s should have survived", k)
		}
	}
}

func TestConcurrentAccess(_ *testing.T) {
	m := NewMemory(16)
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			key := fmt.Sprintf("k%d", i%8)
			m.Put(key, domain.Document{Title: key})
			m.Get(key)
		})
	}
	wg.Wait()
}
