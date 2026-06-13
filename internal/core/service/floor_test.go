package service

import (
	"context"
	"errors"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

const lookupTable = `
# Lookup matches for "*" in /

| Path | Importance | Title | Tags |
|------|------------|-------|------|
| /index.md | 0.95 | demarkus\-soul | index,hub,status:accepted |
| /adr/0005.md | 0.90 | ADR 0005 | adr, decision |
| /scratch/x.md | 0.10 |  | scratch, status:wip |
`

func TestParseCatalogTable(t *testing.T) {
	docs := parseCatalogTable(lookupTable, 10)
	if len(docs) != 3 {
		t.Fatalf("docs = %d, want 3 (%+v)", len(docs), docs)
	}
	if docs[0].Path != "/index.md" || docs[0].Importance != 0.95 || docs[0].Title != "demarkus-soul" {
		t.Errorf("docs[0] = %+v (escaped markdown must be unescaped)", docs[0])
	}
	if docs[0].Status != "accepted" {
		t.Errorf("docs[0].Status = %q, want accepted (status: tag axis)", docs[0].Status)
	}
	if docs[1].Status != "draft" {
		t.Errorf("docs[1].Status = %q, want draft (unlabeled)", docs[1].Status)
	}
	// Empty title falls back to the path basename.
	if docs[2].Title != "x" || docs[2].Status != "wip" {
		t.Errorf("docs[2] = %+v", docs[2])
	}

	if got := parseCatalogTable(lookupTable, 2); len(got) != 2 {
		t.Errorf("limit not applied: %d", len(got))
	}
	if got := parseCatalogTable("status: ok\n\nno table here", 5); got != nil {
		t.Errorf("tableless body should yield nil, got %v", got)
	}
}

func TestFloorAssemblesWorlds(t *testing.T) {
	svc := NewReadingService(fakeGateway{
		worlds: []domain.WorldInfo{{Name: "team-a", URL: "mark://a"}, {Name: "team-b"}},
		raw:    domain.RawDocument{Body: lookupTable},
	}, fakeRenderer{}, nil)

	floor, err := svc.Floor(t.Context())
	if err != nil {
		t.Fatalf("Floor: %v", err)
	}
	if len(floor.Worlds) != 2 {
		t.Fatalf("worlds = %d", len(floor.Worlds))
	}
	if floor.Worlds[0].World.Name != "team-a" || len(floor.Worlds[0].Docs) != 3 {
		t.Errorf("world 0 = %+v", floor.Worlds[0])
	}

	// FloorCached serves the stored copy without touching the gateway.
	var called string
	svc2 := NewReadingService(fakeGateway{called: &called,
		worlds: []domain.WorldInfo{{Name: "team-a"}},
		raw:    domain.RawDocument{Body: lookupTable}}, fakeRenderer{}, nil)
	if _, err := svc2.Floor(t.Context()); err != nil {
		t.Fatalf("Floor: %v", err)
	}
	called = ""
	if _, err := svc2.FloorCached(t.Context()); err != nil {
		t.Fatalf("FloorCached: %v", err)
	}
	if called != "" {
		t.Errorf("FloorCached hit the gateway: %s", called)
	}
}

func TestFloorWorldErrorIsTombstoneNotFailure(t *testing.T) {
	// A world whose catalog read fails (old server rejecting "*", or
	// unreachable) renders dimmed, not dropped — and never kills the floor.
	svc := NewReadingService(fakeGateway{
		worlds: []domain.WorldInfo{{Name: "old-world"}},
		err:    errors.New("bad request: query must be at least 2 characters"),
	}, fakeRenderer{}, nil)

	floor, err := svc.Floor(t.Context())
	if err != nil {
		t.Fatalf("Floor: %v", err)
	}
	if len(floor.Worlds) != 1 || !floor.Worlds[0].Err || floor.Worlds[0].Docs != nil {
		t.Errorf("floor = %+v, want one tombstoned world", floor.Worlds)
	}
}

func TestFloorUnauthorizedPropagates(t *testing.T) {
	svc := NewReadingService(fakeGateway{
		worlds: []domain.WorldInfo{{Name: "team-a"}},
		err:    domain.ErrUnauthorized,
	}, fakeRenderer{}, nil)
	if _, err := svc.Floor(t.Context()); !errors.Is(err, domain.ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized (re-login, not tombstone)", err)
	}

	svc = NewReadingService(fakeGateway{worldsErr: domain.ErrUnauthorized}, fakeRenderer{}, nil)
	if _, err := svc.Floor(context.Background()); !errors.Is(err, domain.ErrUnauthorized) {
		t.Errorf("worlds err = %v, want ErrUnauthorized", err)
	}
}
