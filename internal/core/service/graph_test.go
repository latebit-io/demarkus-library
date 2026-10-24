package service

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus-library/internal/core/port"
)

func ref(world, path string) domain.Ref { return domain.Ref{World: world, Path: path} }

// newGraphService serves one world, "w", that every test reader may see.
func newGraphService() *ReadingService {
	return NewReadingService(fakeGateway{worlds: []domain.WorldInfo{{Name: "w"}}}, nil, nil)
}

func TestRecordLinksAndBacklinks(t *testing.T) {
	svc := newGraphService()

	// a.md and b.md both link to target.md; a.md also links to other.md.
	svc.RecordLinks("w", "/a.md", []domain.Ref{ref("w", "/target.md"), ref("w", "/other.md")})
	svc.RecordLinks("w", "/b.md", []domain.Ref{ref("w", "/target.md")})

	got := svc.Backlinks(context.Background(), "w", "/target.md")
	want := []domain.Ref{ref("w", "/a.md"), ref("w", "/b.md")} // sorted
	if !reflect.DeepEqual(got, want) {
		t.Errorf("backlinks(target) = %v, want %v", got, want)
	}
	if got := svc.Backlinks(context.Background(), "w", "/other.md"); !reflect.DeepEqual(got, []domain.Ref{ref("w", "/a.md")}) {
		t.Errorf("backlinks(other) = %v", got)
	}
	if got := svc.Backlinks(context.Background(), "w", "/nobody.md"); got != nil {
		t.Errorf("backlinks of unlinked doc = %v, want nil", got)
	}
}

func TestRecordLinksReplacesStaleEdges(t *testing.T) {
	svc := newGraphService()
	svc.RecordLinks("w", "/a.md", []domain.Ref{ref("w", "/x.md")})
	// Re-render of a.md: the link to x.md was removed, y.md added. The stale
	// backlink from x.md must not survive (fresh truth replaces it).
	svc.RecordLinks("w", "/a.md", []domain.Ref{ref("w", "/y.md")})

	if got := svc.Backlinks(context.Background(), "w", "/x.md"); got != nil {
		t.Errorf("stale backlink survived: %v", got)
	}
	if got := svc.Backlinks(context.Background(), "w", "/y.md"); !reflect.DeepEqual(got, []domain.Ref{ref("w", "/a.md")}) {
		t.Errorf("new backlink missing: %v", got)
	}
}

func TestRecordLinksSkipsSelfLoops(t *testing.T) {
	svc := newGraphService()
	svc.RecordLinks("w", "/a.md", []domain.Ref{ref("w", "/a.md"), ref("w", "/b.md")})
	if got := svc.Backlinks(context.Background(), "w", "/a.md"); got != nil {
		t.Errorf("self-loop recorded: %v", got)
	}
}

func TestNeighborhood(t *testing.T) {
	svc := newGraphService()
	svc.RecordLinks("w", "/center.md", []domain.Ref{ref("w", "/out1.md"), ref("w", "/out2.md")})
	svc.RecordLinks("w", "/in1.md", []domain.Ref{ref("w", "/center.md")})

	n := svc.Neighborhood(context.Background(), "w", "/center.md")
	if n.Center != ref("w", "/center.md") {
		t.Errorf("center = %v", n.Center)
	}
	if !reflect.DeepEqual(n.Out, []domain.Ref{ref("w", "/out1.md"), ref("w", "/out2.md")}) {
		t.Errorf("out = %v", n.Out)
	}
	if !reflect.DeepEqual(n.In, []domain.Ref{ref("w", "/in1.md")}) {
		t.Errorf("in = %v", n.In)
	}
}

func TestNeighborhoodEmpty(t *testing.T) {
	svc := newGraphService()
	n := svc.Neighborhood(context.Background(), "w", "/lonely.md")
	if n.Out != nil || n.In != nil {
		t.Errorf("unread doc has edges: %+v", n)
	}
}

func TestLinkGraphUnboundedKeepsEverySource(t *testing.T) {
	bySource := make(map[domain.Ref][]domain.Ref, maxLinkGraphSources+5)
	for i := range maxLinkGraphSources + 5 {
		bySource[ref("w", fmt.Sprintf("/src-%d.md", i))] = []domain.Ref{ref("w", "/hub.md")}
	}
	durable := &linkGraph{unbounded: true}
	durable.observeAll(bySource)
	if got := len(durable.backlinks(ref("w", "/hub.md"))); got != maxLinkGraphSources+5 {
		t.Errorf("durable store evicted: %d backlinks, want %d", got, maxLinkGraphSources+5)
	}
	bounded := &linkGraph{}
	bounded.observeAll(bySource)
	if got := len(bounded.backlinks(ref("w", "/hub.md"))); got != maxLinkGraphSources {
		t.Errorf("observed store not capped: %d backlinks, want %d", got, maxLinkGraphSources)
	}
}

// TestObservedGraphMasksHiddenWorlds: the observed map is shared by every
// reader, so a link recorded while reader A read "secret" must not show
// reader B, who may not read it, that world or its paths. Host-shaped worlds
// are the open network and stay visible to both.
func TestObservedGraphMasksHiddenWorlds(t *testing.T) {
	const wiki = "wiki.example.org:6310"
	svc := NewReadingService(fakeGateway{worldsByReader: map[string][]domain.WorldInfo{
		"a": {{Name: "team-a"}, {Name: "secret"}},
		"b": {{Name: "team-a"}},
	}}, nil, nil)
	readerA := port.WithReader(t.Context(), "a")
	readerB := port.WithReader(t.Context(), "b")

	svc.RecordLinks("secret", "/plan.md", []domain.Ref{ref("team-a", "/x.md")})
	svc.RecordLinks("team-a", "/x.md", []domain.Ref{ref("secret", "/plan.md")})
	svc.RecordLinks(wiki, "/pub.md", []domain.Ref{ref("team-a", "/x.md")})

	t.Run("reader A sees both worlds", func(t *testing.T) {
		n := svc.Neighborhood(readerA, "team-a", "/x.md")
		if want := []domain.Ref{ref("secret", "/plan.md"), ref(wiki, "/pub.md")}; !reflect.DeepEqual(n.In, want) {
			t.Errorf("in = %v, want %v", n.In, want)
		}
		if want := []domain.Ref{ref("secret", "/plan.md")}; !reflect.DeepEqual(n.Out, want) {
			t.Errorf("out = %v, want %v", n.Out, want)
		}
	})
	t.Run("reader B: neighbors drop the hidden world", func(t *testing.T) {
		n := svc.Neighborhood(readerB, "team-a", "/x.md")
		if want := []domain.Ref{ref(wiki, "/pub.md")}; !reflect.DeepEqual(n.In, want) {
			t.Errorf("in = %v, want %v", n.In, want)
		}
		if n.Out != nil {
			t.Errorf("out = %v, want nil", n.Out)
		}
	})
	t.Run("reader B: a hidden center has no neighbors", func(t *testing.T) {
		if n := svc.Neighborhood(readerB, "secret", "/plan.md"); n.Degree() != 0 {
			t.Errorf("hidden center leaked edges: %+v", n)
		}
	})
	t.Run("reader B: the floor draws no hidden portal", func(t *testing.T) {
		floor, err := svc.Floor(readerB)
		if err != nil {
			t.Fatalf("Floor: %v", err)
		}
		var portals []string
		for _, w := range floor.Worlds {
			if w.Portal {
				portals = append(portals, w.World.Name)
			}
		}
		if want := []string{"wiki.example.org:6310"}; !reflect.DeepEqual(portals, want) {
			t.Errorf("portals = %v, want %v", portals, want)
		}
		for _, e := range floor.Edges {
			if e.From.World == "secret" || e.To.World == "secret" {
				t.Errorf("floor edge names the hidden world: %+v", e)
			}
		}
	})
}
