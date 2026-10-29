package watch

import (
	"errors"
	"log/slog"
	"time"

	"github.com/latebit-io/demarkus-library/internal/adapter/bearer"
)

// roomPoll is how often a loop waiting on a signed-in reader looks again. The
// look is a local pool check, so polling costs nothing and the first reader's
// feeds start at once instead of after a backed-off retry.
const roomPoll = time.Second

// pacer spaces one loop's retries: doubling from retryMin to retryMax after a
// fault, a steady poll while the room is empty.
type pacer struct {
	retryMin, retryMax, poll time.Duration
	delay                    time.Duration
	waiting                  bool // in an empty-room wait, already reported
}

func newPacer(retryMin, retryMax, poll time.Duration) *pacer {
	return &pacer{retryMin: retryMin, retryMax: retryMax, poll: poll, delay: retryMin}
}

// next returns how long to wait after err, and whether to log it: every fault,
// but an empty room only as its wait begins.
func (p *pacer) next(err error) (wait time.Duration, report bool) {
	if errors.Is(err, bearer.ErrNoReader) {
		report = !p.waiting
		p.waiting = true
		return p.poll, report
	}
	p.waiting = false
	wait = p.delay
	p.delay = min(p.delay*2, p.retryMax)
	return wait, true
}

// reset starts the doubling over after a success.
func (p *pacer) reset() {
	p.delay = p.retryMin
	p.waiting = false
}

// retryLevel logs an empty room at info, since in broker mode it is the normal
// state with nobody signed in, and a fault at warn.
func retryLevel(err error) slog.Level {
	if errors.Is(err, bearer.ErrNoReader) {
		return slog.LevelInfo
	}
	return slog.LevelWarn
}
