package service

import (
	"reflect"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

func ref(world, path string) domain.Ref { return domain.Ref{World: world, Path: path} }

func TestRecordLinksAndBacklinks(t *testing.T) {
	svc := NewReadingService(nil, nil, nil)

	// a.md and b.md both link to target.md; a.md also links to other.md.
	svc.RecordLinks("w", "/a.md", []domain.Ref{ref("w", "/target.md"), ref("w", "/other.md")})
	svc.RecordLinks("w", "/b.md", []domain.Ref{ref("w", "/target.md")})

	got := svc.Backlinks("w", "/target.md")
	want := []domain.Ref{ref("w", "/a.md"), ref("w", "/b.md")} // sorted
	if !reflect.DeepEqual(got, want) {
		t.Errorf("backlinks(target) = %v, want %v", got, want)
	}
	if got := svc.Backlinks("w", "/other.md"); !reflect.DeepEqual(got, []domain.Ref{ref("w", "/a.md")}) {
		t.Errorf("backlinks(other) = %v", got)
	}
	if got := svc.Backlinks("w", "/nobody.md"); got != nil {
		t.Errorf("backlinks of unlinked doc = %v, want nil", got)
	}
}

func TestRecordLinksReplacesStaleEdges(t *testing.T) {
	svc := NewReadingService(nil, nil, nil)
	svc.RecordLinks("w", "/a.md", []domain.Ref{ref("w", "/x.md")})
	// Re-render of a.md: the link to x.md was removed, y.md added. The stale
	// backlink from x.md must not survive (fresh truth replaces it).
	svc.RecordLinks("w", "/a.md", []domain.Ref{ref("w", "/y.md")})

	if got := svc.Backlinks("w", "/x.md"); got != nil {
		t.Errorf("stale backlink survived: %v", got)
	}
	if got := svc.Backlinks("w", "/y.md"); !reflect.DeepEqual(got, []domain.Ref{ref("w", "/a.md")}) {
		t.Errorf("new backlink missing: %v", got)
	}
}

func TestRecordLinksSkipsSelfLoops(t *testing.T) {
	svc := NewReadingService(nil, nil, nil)
	svc.RecordLinks("w", "/a.md", []domain.Ref{ref("w", "/a.md"), ref("w", "/b.md")})
	if got := svc.Backlinks("w", "/a.md"); got != nil {
		t.Errorf("self-loop recorded: %v", got)
	}
}

func TestNeighborhood(t *testing.T) {
	svc := NewReadingService(nil, nil, nil)
	svc.RecordLinks("w", "/center.md", []domain.Ref{ref("w", "/out1.md"), ref("w", "/out2.md")})
	svc.RecordLinks("w", "/in1.md", []domain.Ref{ref("w", "/center.md")})

	n := svc.Neighborhood("w", "/center.md")
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
	svc := NewReadingService(nil, nil, nil)
	n := svc.Neighborhood("w", "/lonely.md")
	if n.Out != nil || n.In != nil {
		t.Errorf("unread doc has edges: %+v", n)
	}
}
