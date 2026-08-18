package domain

import "testing"

func TestIsVersionPath(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"/demarkus-library/plans/reading-room.md/v17", true},
		{"/index.md/v1", true},
		{"/a/b/c.md/v1234", true},
		{"/plans/reading-room.md", false},   // the document itself
		{"/plans/", false},                  // a listing
		{"/notes/v2", false},                // a document named v2, not an edition
		{"/plans/reading-room.md/v", false}, // no number
		{"/plans/reading-room.md/v1a", false},
		{"/plans/reading-room.md/draft", false},
		{"/v1", false}, // no parent document
		{"", false},
	} {
		if got := IsVersionPath(tc.path); got != tc.want {
			t.Errorf("IsVersionPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// A listing and an edition are different things and must not be confused.
func TestIsVersionPathIsDisjointFromListing(t *testing.T) {
	for _, p := range []string{"/plans/reading-room.md/v3", "/plans/", "/plans/x.md"} {
		if IsVersionPath(p) && IsListingPath(p) {
			t.Errorf("%q classified as both an edition and a listing", p)
		}
	}
}
