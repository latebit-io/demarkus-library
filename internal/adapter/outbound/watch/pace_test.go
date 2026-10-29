package watch

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/latebit-io/demarkus-library/internal/adapter/bearer"
)

// Faults double up to the ceiling and are each reported; an empty room polls
// at a steady pace and is reported once per wait; reset starts over.
func TestPacer(t *testing.T) {
	fault := errors.New("dial: connection refused")
	empty := fmt.Errorf("lend: %w", bearer.ErrNoReader)
	p := newPacer(time.Second, 4*time.Second, 10*time.Millisecond)
	steps := []struct {
		err    error
		wait   time.Duration
		report bool
	}{
		{fault, time.Second, true},
		{fault, 2 * time.Second, true},
		{empty, 10 * time.Millisecond, true},
		{empty, 10 * time.Millisecond, false},
		{fault, 4 * time.Second, true},
		{fault, 4 * time.Second, true},
		{empty, 10 * time.Millisecond, true},
	}
	for i, step := range steps {
		wait, report := p.next(step.err)
		if wait != step.wait || report != step.report {
			t.Errorf("step %d (%v): wait %v report %v, want %v %v", i, step.err, wait, report, step.wait, step.report)
		}
	}
	p.reset()
	if wait, _ := p.next(fault); wait != time.Second {
		t.Errorf("after reset: wait %v, want 1s", wait)
	}
}
