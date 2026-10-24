package web

import (
	"testing"
	"time"
)

func TestPendingAsks_FirstTakeStartsLaterTakesAttach(t *testing.T) {
	t.Parallel()

	p := newPendingAsks()
	token, err := p.put(pendingAsk{question: "q", context: "ctx", convKey: "s", expires: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, state := p.take(token, "other"); state != askGone {
		t.Errorf("foreign session took the token: state %d", state)
	}
	fresh, state := p.take(token, "s")
	if state != askFresh || fresh.question != "q" {
		t.Errorf("first take = %+v, state %d; want the question, fresh", fresh, state)
	}
	again, state := p.take(token, "s")
	if state != askAttach {
		t.Errorf("second take state = %d; want attach", state)
	}
	if again.question != "" || again.context != "" {
		t.Errorf("resumed take still holds the question: %+v", again)
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

func TestPendingAsks_ForgetDropsTheToken(t *testing.T) {
	t.Parallel()

	p := newPendingAsks()
	token, err := p.put(pendingAsk{convKey: "s", expires: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	p.take(token, "s")
	p.forget(token)
	if _, state := p.take(token, "s"); state != askGone {
		t.Errorf("take after forget = %d; want gone", state)
	}
}
