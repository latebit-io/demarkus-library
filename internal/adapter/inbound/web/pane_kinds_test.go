package web

import "testing"

// Every kind the trail parses has an entry, since paneLabel and the bare-chunk
// parse read the table unguarded; only a document lacks a context label.
func TestPaneKindsDescribeEveryKind(t *testing.T) {
	tests := []struct {
		kind        string
		bare, pulse bool
	}{
		{paneDoc, false, false},
		{paneGraph, false, false},
		{paneTag, false, false},
		{paneFloor, true, true},
		{paneLibrarian, true, false},
		{paneActivity, true, true},
	}
	if len(paneKinds) != len(tests) {
		t.Errorf("paneKinds has %d kinds, want %d", len(paneKinds), len(tests))
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			k, ok := paneKinds[tt.kind]
			switch {
			case !ok || k.label == nil:
				t.Fatal("no entry with a label")
			case (k.about == nil) != (tt.kind == paneDoc):
				t.Error("only a document names itself by a read")
			case k.bare != tt.bare || k.pulse != tt.pulse:
				t.Errorf("bare, pulse = %v, %v, want %v, %v", k.bare, k.pulse, tt.bare, tt.pulse)
			}
		})
	}
}

// Each kind's palette row and librarian context label, as readers and the model see them.
func TestPaneKindLabels(t *testing.T) {
	tests := []struct {
		name             string
		addr             paneAddr
		title, loc, what string // what: the context label; "" for a document's read
	}{
		{"document", paneAddr{Kind: paneDoc, World: "w.io", Value: "/plans/q4.md"}, "q4", "w.io/plans/q4.md", ""},
		{"graph", paneAddr{Kind: paneGraph, World: "w.io", Value: "/plans/q4.md"}, "graph: q4", "w.io/plans/q4.md", "graph of mark://w.io/plans/q4.md"},
		{"tag", paneAddr{Kind: paneTag, World: "w.io", Value: "ops"}, "#ops", "w.io", "tag page #ops in world w.io"},
		{"floor", paneAddr{Kind: paneFloor}, "universe", "", "the universe floor"},
		{"world map", paneAddr{Kind: paneFloor, World: "w.io"}, "w.io — map", "w.io", "map of world w.io"},
		{"librarian", paneAddr{Kind: paneLibrarian}, "librarian", "", "this librarian conversation"},
		{"activity", paneAddr{Kind: paneActivity}, "activity", "", "the recent activity list"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k := paneKinds[tt.addr.Kind]
			if title, loc := k.label(tt.addr, DefaultTerms()); title != tt.title || loc != tt.loc {
				t.Errorf("label = %q, %q, want %q, %q", title, loc, tt.title, tt.loc)
			}
			if k.about != nil {
				if got := k.about(tt.addr, DefaultTerms()); got != tt.what {
					t.Errorf("context label = %q, want %q", got, tt.what)
				}
			}
		})
	}
}
