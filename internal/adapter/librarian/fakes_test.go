package librarian

// fakePorts is a single stub satisfying the Reader, GraphService, and
// MapService slices with canned data, recording the reads the tools make.

import (
	"context"
	"sync"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

type fakePorts struct {
	mu   sync.Mutex
	raws []string // "world:path" per Raw call

	floor   domain.Floor
	entries []domain.IndexEntry
	raw     domain.RawDocument
	hood    domain.Neighborhood
}

func newFakePorts() *fakePorts {
	return &fakePorts{
		floor: domain.Floor{Worlds: []domain.FloorWorld{{
			World: domain.WorldInfo{Name: "root"},
			Docs:  []domain.FloorDoc{{Path: "/index.md", Title: "root hub", Status: "published"}},
		}}},
		entries: []domain.IndexEntry{
			{Title: "Deploy runbook", Path: "/ops/deploy.md", World: "root", Status: "published"},
			{Title: "Palette notes", Path: "/notes/palette.md", World: "root"},
		},
		raw: domain.RawDocument{
			Source:   "root",
			Path:     "/ops/deploy.md",
			Body:     "# Deploy runbook\n\nSteps live here.",
			Metadata: map[string]string{"title": "Deploy runbook"},
		},
		hood: domain.Neighborhood{
			Center: domain.Ref{World: "root", Path: "/ops/deploy.md"},
			Out:    []domain.Ref{{World: "root", Path: "/ops/rollback.md"}},
		},
	}
}

func (f *fakePorts) rawCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.raws))
	copy(out, f.raws)
	return out
}

// Reader

func (f *fakePorts) Read(context.Context, string, string) (domain.Document, error) {
	return domain.Document{}, nil
}
func (f *fakePorts) Browse(context.Context, string, string) (domain.Document, error) {
	return domain.Document{}, nil
}
func (f *fakePorts) Open(context.Context, string, string) (domain.Document, error) {
	return domain.Document{}, nil
}
func (f *fakePorts) History(context.Context, string, string) (domain.Document, error) {
	return domain.Document{}, nil
}
func (f *fakePorts) Search(context.Context, string, string, string) (domain.Document, error) {
	return domain.Document{}, nil
}
func (f *fakePorts) Tag(context.Context, string, string) (domain.Document, error) {
	return domain.Document{}, nil
}
func (f *fakePorts) Raw(_ context.Context, world, path string) (domain.RawDocument, error) {
	f.mu.Lock()
	f.raws = append(f.raws, world+":"+path)
	f.mu.Unlock()
	return f.raw, nil
}
func (f *fakePorts) NameIndex(context.Context, string, string) ([]domain.IndexEntry, error) {
	return f.entries, nil
}
func (f *fakePorts) ReadCached(context.Context, string, string) (domain.Document, error) {
	return domain.Document{}, nil
}
func (f *fakePorts) BrowseCached(context.Context, string, string) (domain.Document, error) {
	return domain.Document{}, nil
}
func (f *fakePorts) OpenCached(context.Context, string, string) (domain.Document, error) {
	return domain.Document{}, nil
}
func (f *fakePorts) TagCached(context.Context, string, string) (domain.Document, error) {
	return domain.Document{}, nil
}

// GraphService

func (f *fakePorts) RecordLinks(string, string, []domain.Ref) {}
func (f *fakePorts) Backlinks(string, string) []domain.Ref    { return f.hood.In }
func (f *fakePorts) Neighborhood(world, path string) domain.Neighborhood {
	n := f.hood
	n.Center = domain.Ref{World: world, Path: path}
	return n
}

// MapService

func (f *fakePorts) Floor(context.Context) (domain.Floor, error)       { return f.floor, nil }
func (f *fakePorts) FloorCached(context.Context) (domain.Floor, error) { return f.floor, nil }
func (f *fakePorts) WorldMap(context.Context, string) (domain.WorldMap, error) {
	return domain.WorldMap{}, nil
}
func (f *fakePorts) WorldMapCached(context.Context, string) (domain.WorldMap, error) {
	return domain.WorldMap{}, nil
}
