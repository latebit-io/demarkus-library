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
