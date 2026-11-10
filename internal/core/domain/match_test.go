package domain

import (
	"slices"
	"testing"
)

// rankedTitles are the matched entries' titles, best first.
func rankedTitles(q string, entries []IndexEntry, limit int) []string {
	hits := RankIndex(q, entries, limit)
	out := make([]string, 0, len(hits))
	for _, e := range hits {
		out = append(out, e.Title)
	}
	return out
}

// Entries are in the catalog's order, which a tier's ties keep.
func TestRankIndexTiers(t *testing.T) {
	entries := []IndexEntry{
		{Title: "Notes on deployment", Path: "/oauth/notes.md", World: "w"},                // path holds it
		{Title: "Reauthorize tokens", Path: "/a.md", World: "w"},                           // title holds it
		{Title: "Token rotation", Path: "/b.md", World: "w", Tags: []string{"Auth", "x"}},  // a tag is it
		{Title: "OAuth: auth code flow", Path: "/c.md", World: "w"},                        // a word starts with it
		{Title: "Auth middleware", Path: "/d.md", World: "w"},                              // the title starts with it
		{Title: "Session cookies", Path: "/e.md", World: "w", Tags: []string{"authz"}},     // a tag starts with it
		{Title: "Decision record", Path: "/f.md", World: "w", Tags: []string{"kind:auth"}}, // an axis tag's value is it
		{Title: "A unique thing happens", Path: "/g.md", World: "w"},                       // a-u-t-h in order
		{Title: "Unrelated", Path: "/h.md", World: "w"},
	}
	want := []string{
		"Auth middleware", "OAuth: auth code flow", "Token rotation", "Decision record",
		"Reauthorize tokens", "Notes on deployment", "Session cookies", "A unique thing happens",
	}
	if got := rankedTitles("auth", entries, 50); !slices.Equal(got, want) {
		t.Errorf("rank for auth =\n %v\nwant\n %v", got, want)
	}
}

func TestRankIndexTermsAndLimit(t *testing.T) {
	entries := []IndexEntry{
		{Title: "Deploy checklist", Path: "/ops/checklist.md", World: "w"},
		{Title: "Deploy runbook", Path: "/ops/deploy.md", World: "w"},
		{Title: "Rollback runbook", Path: "/ops/rollback.md", World: "w"},
		{Title: "Überblick", Path: "/de/overview.md", World: "w"},
	}
	for _, tt := range []struct {
		name, q string
		limit   int
		want    []string
	}{
		{"equal tiers keep the catalog's order", "deploy", 50, []string{"Deploy checklist", "Deploy runbook"}},
		{"every term must match, in any case and order", "RUNBOOK deploy", 50, []string{"Deploy runbook"}},
		{"a short term is never matched loosely", "dx", 50, []string{}},
		{"a prefix beyond ASCII", "über", 50, []string{"Überblick"}},
		{"a blank query matches nothing", "   ", 50, []string{}},
		{"the limit keeps the best", "runbook", 1, []string{"Deploy runbook"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := rankedTitles(tt.q, entries, tt.limit); !slices.Equal(got, tt.want) {
				t.Errorf("%q = %v, want %v", tt.q, got, tt.want)
			}
		})
	}
}
