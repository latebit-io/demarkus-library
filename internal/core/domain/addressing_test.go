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

func TestResolveHref(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, href string
		want       LinkTarget
		wantOK     bool
	}{
		{name: "relative joins the document dir", href: "rollback.md", want: LinkTarget{Ref: Ref{World: "root", Path: "/ops/rollback.md"}}, wantOK: true},
		{name: "absolute stays in world", href: "/index.md#top", want: LinkTarget{Ref: Ref{World: "root", Path: "/index.md"}, Fragment: "top"}, wantOK: true},
		{name: "mark URL crosses worlds", href: "mark://soul/adr/", want: LinkTarget{Ref: Ref{World: "soul", Path: "/adr/"}}, wantOK: true},
		{name: "bare mark authority is the world root", href: "mark://soul", want: LinkTarget{Ref: Ref{World: "soul", Path: "/"}}, wantOK: true},
		{name: "percent-encoded version path decodes", href: "%2Fops%2Fdeploy.md/v2", want: LinkTarget{Ref: Ref{World: "root", Path: "/ops/deploy.md/v2"}}, wantOK: true},
		{name: "escaped hash stays in the file name", href: "a%23b.md#top", want: LinkTarget{Ref: Ref{World: "root", Path: "/ops/a#b.md"}, Fragment: "top"}, wantOK: true},
		{name: "escaped question mark stays in the file name", href: "a%3Fb.md", want: LinkTarget{Ref: Ref{World: "root", Path: "/ops/a?b.md"}}, wantOK: true},
		{name: "external scheme is not ours", href: "https://example.com/x.md"},
		{name: "in-page anchor is not a target", href: "#steps"},
		{name: "empty href", href: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := ResolveHref(tt.href, "root", "/ops/deploy.md")
			if ok != tt.wantOK || got != tt.want {
				t.Errorf("ResolveHref(%q) = %+v, %v; want %+v, %v", tt.href, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
