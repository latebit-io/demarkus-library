package web

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// graphWrites is each write event a stream sent, in order.
func graphWrites(body string) []string {
	var writes []string
	for frame := range strings.SplitSeq(body, "\n\n") {
		if data, ok := strings.CutPrefix(frame, "event: write\ndata: "); ok {
			writes = append(writes, data)
		}
	}
	return writes
}

// The list at connect is what the open graph already draws; each write after
// it is named once, oldest first, a first edition marked new.
func TestLiveGraphNamesNewWrites(t *testing.T) {
	t.Parallel()
	drawn := pulseOf(domain.OpAppend, "team-a", "/drawn.md", 4)
	created := pulseOf(domain.OpPublish, "team-a", "/new.md", 1)
	archived := pulseOf(domain.OpArchive, "team-b", "/old.md", 3)
	rewritten := pulseOf(domain.OpPublish, "team-a", "/drawn.md", 5)
	live := &fakeWatcher{pulseSignals: 2, pulses: [][]domain.Pulse{
		{drawn},
		{created, drawn},
		{rewritten, archived, created},
	}}
	body := get(liveApp(t, LiveHandler{pulse: live}), LiveGraphPath).Body.String()
	want := []string{
		"new=1&op=publish&path=%2Fnew.md&world=team-a",
		"op=archive&path=%2Fold.md&world=team-b",
		"op=publish&path=%2Fdrawn.md&world=team-a",
	}
	if got := graphWrites(body); !slices.Equal(got, want) {
		t.Errorf("writes = %q, want %q:\n%s", got, want, body)
	}
}

func TestLiveGraphRefusals(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		h      LiveHandler
		header http.Header
		want   int
	}{
		{"cross-site fetch", LiveHandler{pulse: &fakeWatcher{}}, http.Header{"Sec-Fetch-Site": {"cross-site"}}, http.StatusForbidden},
		{"no change feed", LiveHandler{}, nil, http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if rec := liveGet(liveApp(t, tt.h), LiveGraphPath, tt.header); rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

// A host-shaped world is named with its port on both sides, so a write in
// soul.demarkus.io lands on a graph drawn for soul.demarkus.io:6309.
func TestLiveGraphKeysHostWorlds(t *testing.T) {
	t.Parallel()
	if got := graphWrite(pulseOf(domain.OpAppend, "soul.demarkus.io", "/a.md", 2)); !strings.Contains(got, "world=soul.demarkus.io%3A6309") {
		t.Errorf("write = %q, want the keyed world", got)
	}
	n := domain.Neighborhood{Center: domain.Ref{World: "soul.demarkus.io", Path: "/a.md"},
		Out: []domain.Ref{{World: "Soul.Demarkus.io:6309", Path: "/b.md"}}}
	graph := string(graphSVG(n, func(r domain.Ref) string { return docRoute(r.World, r.Path) }, nil, nil))
	if strings.Count(graph, `data-world="soul.demarkus.io:6309"`) != 2 {
		t.Errorf("centre and neighbour not keyed alike:\n%s", graph)
	}
}

// The canvas names the stream only with a change feed, so a page without one
// never retries a stream that would 404.
func TestCanvasCarriesTheLiveGraphStream(t *testing.T) {
	t.Parallel()
	doc := domain.Document{Title: "X", Path: "/x.md", HTML: "<p>x</p>", Version: "3"}
	const attr = `data-live-graph="` + LiveGraphPath + `"`
	if body := get(liveCanvasApp(t, doc), "/t/soul.demarkus.io/d/x.md").Body.String(); !strings.Contains(body, attr) {
		t.Errorf("live canvas lacks %s", attr)
	}
	plain := roomApp(t, NewRoom(&fakeReading{doc: doc}, "soul.demarkus.io", "/index.md"))
	if body := get(plain, "/t/soul.demarkus.io/d/x.md").Body.String(); strings.Contains(body, "data-live-graph") {
		t.Error("a canvas without a change feed names the live graph")
	}
}

// Nodes say which world they belong to, so a write lands on the right one: a
// local graph's neighbour may live in another world than its centre.
func TestGraphNodesNameTheirWorld(t *testing.T) {
	t.Parallel()
	n := domain.Neighborhood{
		Center: domain.Ref{World: "team-a", Path: "/center.md"},
		Out:    []domain.Ref{{World: "team-b", Path: "/out.md"}},
	}
	graph := string(graphSVG(n, func(r domain.Ref) string { return docRoute(r.World, r.Path) }, nil, nil))
	for _, want := range []string{`aria-label="document neighborhood" data-world="team-a"`, `data-node="/out.md" data-world="team-b"`} {
		if !strings.Contains(graph, want) {
			t.Errorf("local graph lacks %s:\n%s", want, graph)
		}
	}
	wm := testWorldMap()
	world := string(worldMapSVG(wm, func(p string) string { return p }, ""))
	if want := `data-world="` + wm.World.Name + `"`; !strings.Contains(world, want) {
		t.Errorf("world map lacks %s:\n%s", want, world)
	}
}
