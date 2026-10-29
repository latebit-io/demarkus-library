package web

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSSEWriter_KeepaliveUntilClosed(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var buf strings.Builder
	w := &lockedWriter{mu: &mu, w: &buf}
	out := newSSEWriter(context.Background(), w, func() {}, 5*time.Millisecond)
	time.Sleep(30 * time.Millisecond)
	out.close()
	mu.Lock()
	n := strings.Count(buf.String(), ": keepalive\n\n")
	mu.Unlock()
	if n == 0 {
		t.Fatal("no keepalive frames written")
	}
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if strings.Count(buf.String(), ": keepalive\n\n") != n {
		t.Error("keepalive kept writing after close")
	}
}

// lockedWriter lets the test read the buffer while the keepalive writes it.
type lockedWriter struct {
	mu *sync.Mutex
	w  *strings.Builder
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// Every SSE line ending becomes a data line: a bare \r must not start a line
// the client reads as a field, or end the event early.
func TestSSEDataFoldsEveryLineEnding(t *testing.T) {
	got := sseData("a\rb\r\nc\n\revent: x")
	want := "data: a\ndata: b\ndata: c\ndata: \ndata: event: x"
	if got != want {
		t.Errorf("sseData = %q, want %q", got, want)
	}
}
