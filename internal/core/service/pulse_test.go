package service

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus-library/internal/core/port"
)

func pulsePaths(pulses []domain.Pulse) []string {
	paths := make([]string, 0, len(pulses))
	for _, p := range pulses {
		paths = append(paths, p.World+p.Path)
	}
	return paths
}

// The log lists each document once, at its latest write under any spelling of
// its world, newest first, and keeps only the latest pulseCap.
func TestPulseLogKeepsEachDocumentOnce(t *testing.T) {
	var l pulseLog
	write := func(world, path, op string) {
		l.record(domain.Pulse{Change: domain.Change{World: world, Path: path, Op: op}})
	}
	write("h.example.org", "/a.md", "publish")
	write("h.example.org", "/b.md", "publish")
	write("h.example.org:6309", "/a.md", "append")
	got := l.visible(hostIndex{})
	if want := []string{"h.example.org:6309/a.md", "h.example.org/b.md"}; !slices.Equal(pulsePaths(got), want) {
		t.Fatalf("pulse = %v, want %v", pulsePaths(got), want)
	}
	if got[0].Op != "append" {
		t.Errorf("a.md shows %q, want its latest write", got[0].Op)
	}

	for i := range pulseCap + 3 {
		write("h.example.org", fmt.Sprintf("/%d.md", i), "publish")
	}
	got = l.visible(hostIndex{})
	if len(got) != pulseCap || got[0].Path != fmt.Sprintf("/%d.md", pulseCap+2) {
		t.Errorf("after a flood: %d entries, newest %s; want %d, newest last written", len(got), got[0].Path, pulseCap)
	}
}

// A reader's pulse lists writes only in worlds their world list carries (a
// host is the open network); a resync adds nothing, and each write signals.
func TestPulseMasksByTheReadersWorlds(t *testing.T) {
	ctx := port.WithReader(t.Context(), "a")
	svc := NewReadingService(fakeGateway{worlds: []domain.WorldInfo{{Name: "team-a"}}}, fakeRenderer{}, nil)
	signal := svc.WatchPulse(ctx)

	svc.FollowChanges(ctx, fakeFeed{
		{World: "team-a", Path: "/a.md", Op: "publish"},
		{World: "team-b", Path: "/b.md", Op: "publish"},
		{World: "team-a", Resync: true},
		{World: "h.example.org", Path: "/c.md", Op: "archive"},
	})

	got, err := svc.Pulse(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"h.example.org/c.md", "team-a/a.md"}; !slices.Equal(pulsePaths(got), want) {
		t.Errorf("pulse = %v, want %v", pulsePaths(got), want)
	}
	if !pending(signal) {
		t.Error("pulse watcher not signalled")
	}
}

// A derived write, such as a world-graph checkpoint, is no one's activity: it
// neither lists nor signals.
func TestPulseSkipsDerivedWrites(t *testing.T) {
	ctx := port.WithReader(t.Context(), "a")
	svc := NewReadingService(fakeGateway{worlds: []domain.WorldInfo{{Name: "team-a"}}}, fakeRenderer{}, nil)
	signal := svc.WatchPulse(ctx)

	svc.FollowChanges(ctx, fakeFeed{{World: "team-a", Path: "/graph/worlds/team-a/9.md", Op: "publish", Derived: true}})

	got, err := svc.Pulse(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("pulse = %v, want none", pulsePaths(got))
	}
	if pending(signal) {
		t.Error("pulse watcher signalled by a derived write")
	}
}

// A reader whose world list fails gets the error, never an unmasked pulse.
func TestPulseFailsWithTheWorldList(t *testing.T) {
	ctx := port.WithReader(t.Context(), "a")
	svc := NewReadingService(fakeGateway{worldsErr: domain.ErrUnauthorized}, fakeRenderer{}, nil)
	svc.pulse.record(domain.Pulse{Change: domain.Change{World: "team-a", Path: "/a.md"}})
	if got, err := svc.Pulse(ctx); !errors.Is(err, domain.ErrUnauthorized) || got != nil {
		t.Errorf("Pulse = %v, %v; want nothing and unauthorized", got, err)
	}
}
