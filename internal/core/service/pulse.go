package service

import (
	"context"
	"slices"
	"sync"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// pulseCap is how many recent writes the floor's pulse lists.
const pulseCap = 12

// pulseLog is the pod's recent writes, newest first, each document once at
// its latest write. Every pod watches every world, so replicas hold the same
// log; a restart starts it empty.
type pulseLog struct {
	mu     sync.Mutex
	recent []domain.Pulse
}

func (l *pulseLog) record(p domain.Pulse) {
	world := worldKey(p.World)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.recent = slices.DeleteFunc(l.recent, func(old domain.Pulse) bool {
		return old.Path == p.Path && worldKey(old.World) == world
	})
	l.recent = slices.Insert(l.recent, 0, p)
	l.recent = l.recent[:min(len(l.recent), pulseCap)]
}

// visible copies the writes in worlds the reader may see.
func (l *pulseLog) visible(hosts hostIndex) []domain.Pulse {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []domain.Pulse
	for _, p := range l.recent {
		if hosts.visible(p.World) {
			out = append(out, p)
		}
	}
	return out
}

// Pulse implements port.Pulse: the recent writes masked by the reader's world
// list, the floor's own mask.
func (s *ReadingService) Pulse(ctx context.Context) ([]domain.Pulse, error) {
	hosts, err := s.readerHosts(ctx)
	if err != nil {
		return nil, err
	}
	return s.pulse.visible(hosts), nil
}

// WatchPulse implements port.Pulse. Every write signals, also one in a world
// the reader cannot see: the caller compares what it lists, not the signal.
func (s *ReadingService) WatchPulse(ctx context.Context) <-chan struct{} {
	return s.watchers.add(ctx, func(change domain.Change) bool { return !change.Resync })
}
