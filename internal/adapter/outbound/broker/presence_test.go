package broker

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/latebit-io/demarkus-library/internal/adapter/bearer"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

func TestPresentParsesThePeople(t *testing.T) {
	const head = "status: ok\nwindow: 20m\n"
	tests := []struct {
		name string
		text string
		want []domain.Presence
	}{
		{"people", head + "count: 2\n\n| user | clients | seen |\n|------|---------|------|\n" +
			"| ana@example.org | claude-code, demarkus-library | 2026-11-19T12:36:00Z |\n" +
			"| a\\|b@example.org | unknown | 2026-11-19T12:30:00Z |\n" +
			"| late@example.org | claude-code | soon |\n",
			[]domain.Presence{
				{User: "ana@example.org", Clients: []string{"claude-code", "demarkus-library"}, Seen: time.Date(2026, 11, 19, 12, 36, 0, 0, time.UTC)},
				{User: "a|b@example.org", Clients: []string{"unknown"}, Seen: time.Date(2026, 11, 19, 12, 30, 0, 0, time.UTC)},
			}},
		// No one listed: the broker sends no table.
		{"no one", head + "count: 0\n", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := &fakeCaller{text: tt.text}
			people, err := (&Gateway{caller: fc}).Present(authedCtx(t))
			if err != nil {
				t.Fatalf("Present: %v", err)
			}
			if fc.gotTool != "mark_presence" || fc.gotToken != "tok-123" {
				t.Errorf("called %q with token %q", fc.gotTool, fc.gotToken)
			}
			if len(people) != len(tt.want) || (len(people) > 0 && !reflect.DeepEqual(people, tt.want)) {
				t.Errorf("people = %+v\nwant     %+v", people, tt.want)
			}
		})
	}
}

// A broker older than the tool says so on a live session: the answer is its
// own error, and asking again must not cost the reader's session a rebuild.
func TestPresentOnABrokerWithoutTheTool(t *testing.T) {
	srv := newMCPTestServer(t)
	g := srv.gateway()
	defer g.Close()
	ctx := bearer.WithToken(t.Context(), "good-token")
	for range 3 {
		if people, err := g.Present(ctx); !errors.Is(err, domain.ErrPresenceUnsupported) || people != nil {
			t.Fatalf("Present = %+v, %v; want ErrPresenceUnsupported", people, err)
		}
	}
	if _, err := g.Fetch(ctx, "soul", "/index.md"); err != nil {
		t.Fatalf("Fetch after: %v", err)
	}
	if n := srv.handshakeCount(); n != 1 {
		t.Errorf("handshake count = %d, want 1: an unknown tool rebuilt the session", n)
	}
}
