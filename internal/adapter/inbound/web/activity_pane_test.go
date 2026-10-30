package web

import (
	"html"
	"net/http"
	"strings"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// The activity pane lists the reader's pulse beside any pane, its links
// continuing the trail from it and the dock naming it; only the focused pane
// streams.
func TestActivityPane(t *testing.T) {
	live := &fakeWatcher{pulses: [][]domain.Pulse{{pulseOf(domain.OpPublish, "team-a", "/plans/q4.md", 3)}}}
	beside := trail{Panes: []paneAddr{doc("w.io", "/x.md"), {Kind: paneActivity}}, Focus: 1, Reader: -1, Meta: -1}
	tests := []struct {
		name       string
		path       string
		want       []string
		wantStream string
	}{
		{"focused beside a doc", "/t/w.io/d/x.md/~/v", []string{
			`<a href="/t/w.io/d/x.md/~/v/~/team-a/d/plans/q4.md">/plans/q4.md</a>`,
			`<a class="dock-link" href="/t/w.io/d/x.md/~/v">activity</a>`,
		}, livePulseURL(beside, 1)},
		{"left of the focus", "/t/v/~/w.io/d/x.md", []string{
			`<a href="/t/v/~/team-a/d/plans/q4.md">/plans/q4.md</a>`,
		}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := get(roomApp(t, pulseRoom(live)), tt.path).Body.String()
			for _, want := range tt.want {
				if !strings.Contains(body, want) {
					t.Errorf("canvas lacks %q", want)
				}
			}
			if got := pulseStreamURL(body); got != tt.wantStream {
				t.Errorf("stream = %q, want %q", got, tt.wantStream)
			}
		})
	}
}

// pulseStreamURL is the pulse's stream in body, "" when none connects.
func pulseStreamURL(body string) string {
	for _, m := range sseConnect.FindAllStringSubmatch(body, -1) {
		if strings.HasPrefix(m[1], LivePulsePath) {
			return html.UnescapeString(m[1])
		}
	}
	return ""
}

// A room without a change feed says the list is unavailable, with no stream;
// a refused list refuses the focused pane.
func TestActivityPaneWithoutAList(t *testing.T) {
	off := get(readingApp(t, &fakeReading{floor: testFloor()}), "/t/v").Body.String()
	if !strings.Contains(off, "Recent activity is unavailable right now") || pulseStreamURL(off) != "" {
		t.Errorf("no-feed pane = %s", off)
	}
	refused := get(roomApp(t, pulseRoom(&fakeWatcher{pulseErr: domain.ErrUnauthorized})), "/t/v")
	if refused.Code != http.StatusUnauthorized {
		t.Errorf("refused status = %d, want %d", refused.Code, http.StatusUnauthorized)
	}
}

// The nav's activity door joins the current trail, opens a fresh one off the
// canvas, and shows only with a change feed.
func TestActivityDoor(t *testing.T) {
	doors := map[string]string{
		"/t/u":               `<a class="nav-key nav-activity" href="/t/u/~/v"`,
		"/w/w.io/search?q=x": `<a class="nav-key nav-activity" href="/t/v"`,
	}
	for path, door := range doors {
		if body := get(roomApp(t, pulseRoom(&fakeWatcher{})), path).Body.String(); !strings.Contains(body, door) {
			t.Errorf("%s lacks the activity door %q", path, door)
		}
	}
	if body := get(readingApp(t, &fakeReading{floor: testFloor()}), "/t/u").Body.String(); strings.Contains(body, "nav-activity") {
		t.Error("a room without a change feed shows the activity door")
	}
}
