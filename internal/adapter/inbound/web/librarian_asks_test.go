package web

import (
	"testing"
	"time"
)

func TestPendingAsks_ReconnectGrace(t *testing.T) {
	t.Parallel()

	p := newPendingAsks()
	p.grace = 20 * time.Millisecond
	token, err := p.put(pendingAsk{convKey: "s", expires: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, state := p.take(token, "other"); state != askGone {
		t.Errorf("foreign session took the token: state %d", state)
	}
	if _, state := p.take(token, "s"); state != askFresh {
		t.Errorf("first take state = %d; want fresh", state)
	}
	stopped := make(chan struct{}, 2)
	p.hold(token, func() { stopped <- struct{}{} })

	// A reconnect inside the grace keeps the run alive.
	p.detach(token)
	if _, state := p.take(token, "s"); state != askResumed {
		t.Errorf("second take state = %d; want resumed", state)
	}
	select {
	case <-stopped:
		t.Fatal("run stopped despite a reconnect inside the grace")
	case <-time.After(3 * p.grace):
	}

	// No reconnect: the grace ends the run.
	p.detach(token)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("run not stopped after the grace elapsed")
	}
}

func TestPendingAsks_GraceWaitsForEveryStream(t *testing.T) {
	t.Parallel()

	p := newPendingAsks()
	p.grace = 20 * time.Millisecond
	token, err := p.put(pendingAsk{convKey: "s", expires: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	p.take(token, "s") // the first stream
	// A duplicate connection resumes and drops before the first stream
	// holds the run: no timer, and the count must not go stale.
	p.take(token, "s")
	p.detach(token)
	stopped := make(chan struct{}, 2)
	p.hold(token, func() { stopped <- struct{}{} })

	// A second resume attaches; the first stream dropping must not end the
	// run under it.
	p.take(token, "s")
	p.detach(token)
	select {
	case <-stopped:
		t.Fatal("run stopped while a resumed stream was attached")
	case <-time.After(3 * p.grace):
	}
	// The last stream drops: the grace runs.
	p.detach(token)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("run not stopped after the last stream detached")
	}
	// A stray extra detach is ignored, not a negative count.
	p.detach(token)
}

func TestPendingAsks_ExpiredIsGone(t *testing.T) {
	t.Parallel()

	p := newPendingAsks()
	token, err := p.put(pendingAsk{convKey: "s", expires: time.Now().Add(-time.Second)})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, state := p.take(token, "s"); state != askGone {
		t.Errorf("expired take state = %d; want gone", state)
	}
}
