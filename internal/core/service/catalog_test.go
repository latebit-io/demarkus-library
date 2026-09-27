package service

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

func TestLookupParsesHits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		query     domain.CatalogQuery
		raw       domain.RawDocument
		wantCall  string
		wantHits  []domain.CatalogHit
		wantBody  bool
		wantError bool // a partial universe answer
	}{
		{
			name:     "body match in one world names section and snippet",
			query:    domain.CatalogQuery{World: "soul", Query: "sysctl", Match: domain.MatchBody},
			wantCall: "Lookup",
			raw: domain.RawDocument{
				Metadata: map[string]string{"match": "body"},
				Body: "| Path | Importance | Title | Tags | Snippet |\n|---|---|---|---|---|\n" +
					"| /debugging.md#poison-lock | 0.70 | Debugging | ops, status:published | raise the sysctl first |\n",
			},
			wantBody: true,
			wantHits: []domain.CatalogHit{{
				Ref:    domain.Ref{World: "soul", Path: "/debugging.md"},
				Anchor: "poison-lock", Title: "Debugging", Tags: []string{"ops", "status:published"},
				Status: "published", Importance: 0.7, Snippet: "raise the sysctl first",
			}},
		},
		{
			name:     "universe rows carry their world and section",
			query:    domain.CatalogQuery{Query: "deploy", Match: domain.MatchBody},
			wantCall: "LookupAll",
			raw: domain.RawDocument{
				Metadata: map[string]string{"match": "body"},
				Body: "| Path | Importance | Title | Tags | Snippet |\n|---|---|---|---|---|\n" +
					"| mark://root/ops/deploy.md#rollback | 0.50 | Deploy | ops | roll back with helm |\n",
			},
			wantBody: true,
			wantHits: []domain.CatalogHit{{
				Ref:    domain.Ref{World: "root", Path: "/ops/deploy.md"},
				Anchor: "rollback", Title: "Deploy", Tags: []string{"ops"},
				Status: "draft", Importance: 0.5, Snippet: "roll back with helm",
			}},
		},
		{
			name:     "a world without body match answers from the catalog",
			query:    domain.CatalogQuery{World: "soul", Query: "deploy", Match: domain.MatchBody},
			wantCall: "Lookup",
			raw: domain.RawDocument{Body: "| Path | Importance | Title | Tags |\n|---|---|---|---|\n" +
				"| /ops/deploy.md | 0.50 | Deploy | ops |\n"},
			wantHits: []domain.CatalogHit{{
				Ref: domain.Ref{World: "soul", Path: "/ops/deploy.md"}, Title: "Deploy",
				Tags: []string{"ops"}, Status: "draft", Importance: 0.5,
			}},
		},
		{
			name:     "partial universe answer keeps its hits",
			query:    domain.CatalogQuery{Query: "deploy"},
			wantCall: "LookupAll",
			raw: domain.RawDocument{
				Metadata: map[string]string{"status": "partial", "worlds": "2", "failed": "1"},
				Body: "| Path | Importance | Title | Tags |\n|---|---|---|---|\n" +
					"| mark://root/ops/deploy.md | 0.50 | Deploy | ops |\n",
			},
			wantHits: []domain.CatalogHit{{
				Ref: domain.Ref{World: "root", Path: "/ops/deploy.md"}, Title: "Deploy",
				Tags: []string{"ops"}, Status: "draft", Importance: 0.5,
			}},
			wantError: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var called string
			svc := newTestService(fakeGateway{raw: tt.raw, called: &called}, fakeRenderer{})
			got, err := svc.Lookup(context.Background(), tt.query)
			var partial *domain.PartialLookupError
			if tt.wantError != errors.As(err, &partial) {
				t.Fatalf("err = %v, want partial %v", err, tt.wantError)
			}
			if called != tt.wantCall {
				t.Errorf("gateway call = %q, want %q", called, tt.wantCall)
			}
			if got.BodyMatched != tt.wantBody {
				t.Errorf("BodyMatched = %v, want %v", got.BodyMatched, tt.wantBody)
			}
			if !reflect.DeepEqual(got.Hits, tt.wantHits) {
				t.Errorf("hits = %+v\nwant %+v", got.Hits, tt.wantHits)
			}
		})
	}
}

func TestLookupTagNarrowsAndRanks(t *testing.T) {
	t.Parallel()

	var filter string
	svc := newTestService(fakeGateway{filter: &filter}, fakeRenderer{})
	if _, err := svc.Lookup(context.Background(), domain.CatalogQuery{World: "soul", Tag: "adr"}); err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if filter != "tag=adr" {
		t.Errorf("filter = %q, want tag=adr", filter)
	}
	if _, err := svc.Lookup(context.Background(), domain.CatalogQuery{World: "soul"}); err == nil {
		t.Error("a lookup with neither query nor tag must be refused")
	}
}
