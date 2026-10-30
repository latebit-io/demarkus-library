package web

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

var pulseAt = time.Date(2026, 10, 30, 12, 36, 0, 0, time.UTC)

func pulseOf(op, world, path string, version int) domain.Pulse {
	return domain.Pulse{At: pulseAt, Change: domain.Change{World: world, Path: path, Op: op, Version: version,
		Agent: "claude-code", User: "ana@example.org"}}
}

var floorTrail = trail{Panes: []paneAddr{{Kind: paneFloor}}, Reader: -1, Meta: -1}

// The focused floor lists the reader's pulse, its links continuing the trail,
// and connects the stream that keeps it current.
func TestFloorListsThePulse(t *testing.T) {
	live := &fakeWatcher{pulses: [][]domain.Pulse{{
		pulseOf(domain.OpPublish, "team-a", "/plans/q4.md", 3),
		pulseOf(domain.OpArchive, "team-a", "/old.md", 0),
	}}}
	body := get(roomApp(t, pulseRoom(live)), "/t/u").Body.String()
	for _, want := range []string{
		`<h2 id="pulse-head" class="pulse-head">Recent activity</h2>`,
		`<time datetime="2026-10-30T12:36:00Z">12:36 UTC</time>`,
		`<a href="/t/u/~/team-a/d/plans/q4.md">/plans/q4.md</a>`,
		`<span class="pulse-what">published · edition 3</span>`,
		`<span class="pulse-by person" title="written by">ana@example.org</span>`,
		`<span class="pulse-by agent" title="written with">claude-code</span>`,
		`<span class="pulse-gone">/old.md</span>`, // an archived document has nothing to open
	} {
		if !strings.Contains(body, want) {
			t.Errorf("floor missing %q", want)
		}
	}
	if got, want := streamURL(body), livePulseURL(floorTrail, 0); got != want {
		t.Errorf("stream = %q, want %q", got, want)
	}
}

// Only a focused floor with a change feed carries the pulse.
func TestFloorPulseNeedsFocusAndAFeed(t *testing.T) {
	svc := &fakeReading{floor: testFloor(), doc: domain.Document{Title: "Doc", Path: "/x.md", HTML: "<p>x</p>"}}
	live := &fakeWatcher{pulses: [][]domain.Pulse{{pulseOf(domain.OpPublish, "team-a", "/a.md", 1)}}}
	tests := []struct {
		name string
		room Room
		path string
	}{
		{"no change feed", NewRoom(svc, "soul.demarkus.io", "/index.md"), "/t/u"},
		{"unfocused floor", NewRoom(svc, "soul.demarkus.io", "/index.md").WithLiveEditions(live), "/t/u/~/w.io/d/x.md"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if body := get(roomApp(t, tt.room), tt.path).Body.String(); strings.Contains(body, `class="pulse"`) {
				t.Error("floor carries a pulse")
			}
		})
	}
}

// A failed list says so and still mounts the stream, whose retry swaps the
// list in.
func TestFloorPulseOnAFailedList(t *testing.T) {
	body := get(roomApp(t, pulseRoom(&fakeWatcher{pulseFails: 1})), "/t/u").Body.String()
	for _, want := range []string{
		`<li class="pulse-quiet">Recent activity is unavailable right now</li>`,
		`hx-sse:connect="`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("floor missing %q", want)
		}
	}
	if strings.Contains(body, "Nothing written") {
		t.Error("a failed list reads as a quiet room")
	}
}

// A refused list refuses the floor, so the reader signs in again rather than
// reading a floor that lost its pulse.
func TestFloorPulseRefusedRefusesTheFloor(t *testing.T) {
	rec := get(roomApp(t, pulseRoom(&fakeWatcher{pulseErr: domain.ErrUnauthorized})), "/t/u")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// pulseRoom is a room over the floor whose change feed is live.
func pulseRoom(live *fakeWatcher) Room {
	return NewRoom(&fakeReading{floor: testFloor()}, "soul.demarkus.io", "/index.md").WithLiveEditions(live)
}

func TestLivePulseStream(t *testing.T) {
	t.Parallel()
	a, b := pulseOf(domain.OpPublish, "team-a", "/a.md", 1), pulseOf(domain.OpAppend, "team-a", "/b.md", 2)
	tests := []struct {
		name       string
		pulses     [][]domain.Pulse
		err        error
		fails      int
		signals    int
		wantFrames int
		wantDone   bool
	}{
		{name: "the first list goes out at connect", pulses: [][]domain.Pulse{{a}}, wantFrames: 1},
		{name: "an empty first list still goes out", pulses: [][]domain.Pulse{{}}, wantFrames: 1},
		{name: "a failed list retries with no write to prompt it", pulses: [][]domain.Pulse{{a}}, fails: 1, wantFrames: 1},
		{name: "a write the reader cannot see sends nothing", pulses: [][]domain.Pulse{{a}, {a}}, signals: 1, wantFrames: 1},
		{name: "a write the reader sees swaps the list", pulses: [][]domain.Pulse{{a}, {b, a}}, signals: 1, wantFrames: 2},
		{name: "refused at connect ends the stream", err: domain.ErrUnauthorized, signals: 1, wantDone: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			live := &fakeWatcher{pulses: tt.pulses, pulseErr: tt.err, pulseFails: tt.fails, pulseSignals: tt.signals}
			rec := get(roomApp(t, pulseRoom(live)), livePulseURL(floorTrail, 0))
			body := rec.Body.String()
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", rec.Code, body)
			}
			if got := strings.Count(body, `hx-target="find .pulse-list"`); got != tt.wantFrames {
				t.Errorf("frames = %d, want %d:\n%s", got, tt.wantFrames, body)
			}
			if got := strings.Contains(body, "event: done"); got != tt.wantDone {
				t.Errorf("done sent = %v, want %v", got, tt.wantDone)
			}
		})
	}
}

func TestLivePulseRefusals(t *testing.T) {
	t.Parallel()
	live := pulseRoom(&fakeWatcher{})
	tests := []struct {
		name   string
		room   Room
		target string
		want   int
	}{
		{"no change feed", NewRoom(&fakeReading{}, "soul.demarkus.io", "/index.md"), livePulseURL(floorTrail, 0), http.StatusNotFound},
		{"not the floor", live, LivePulsePath + "?trail=w.io%2Fd%2Fx.md&idx=0", http.StatusBadRequest},
		{"junk trail", live, LivePulsePath + "?trail=%2F%2F&idx=0", http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rec := get(roomApp(t, tt.room), tt.target); rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestPulseWhat(t *testing.T) {
	tests := []struct {
		name   string
		change domain.Change
		want   string
	}{
		{"publish", domain.Change{Op: domain.OpPublish, Version: 3}, "published · edition 3"},
		{"append", domain.Change{Op: domain.OpAppend, Version: 4}, "appended · edition 4"},
		{"archive drops the edition", domain.Change{Op: domain.OpArchive, Version: 5}, "archived"},
		{"unknown op", domain.Change{Op: "rename"}, "written"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pulseWhat(tt.change); got != tt.want {
				t.Errorf("pulseWhat(%+v) = %q, want %q", tt.change, got, tt.want)
			}
		})
	}
}

// After a list the stream waits pulseSettle before the next, so a burst of
// writes coalesces into the watcher's one pending signal.
func TestLivePulseSettlesBetweenLists(t *testing.T) {
	t.Parallel()
	live := &fakeWatcher{pulses: [][]domain.Pulse{{pulseOf(domain.OpPublish, "team-a", "/a.md", 1)}}, pulseSignals: 1}
	start := time.Now()
	get(roomApp(t, pulseRoom(live)), livePulseURL(floorTrail, 0))
	if elapsed := time.Since(start); elapsed < pulseSettle {
		t.Errorf("stream ended %v after a list, want at least %v", elapsed, pulseSettle)
	}
}
