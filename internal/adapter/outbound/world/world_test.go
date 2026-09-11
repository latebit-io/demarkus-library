package world

import (
	"errors"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus/client/fetch"
	"github.com/latebit-io/demarkus/protocol"
)

type fakeClient struct {
	res fetch.Result
	err error
}

func (f fakeClient) Fetch(string, string, string) (fetch.Result, error)    { return f.res, f.err }
func (f fakeClient) List(string, string, string) (fetch.Result, error)     { return f.res, f.err }
func (f fakeClient) Versions(string, string, string) (fetch.Result, error) { return f.res, f.err }
func (f fakeClient) Lookup(lookupCall) (fetch.Result, error) {
	return f.res, f.err
}

func newGateway(status, body string, meta map[string]string) *Gateway {
	return NewGateway(fakeClient{res: fetch.Result{Response: protocol.Response{
		Status:   status,
		Body:     body,
		Metadata: meta,
	}}}, "soul.demarkus.io", "")
}

func TestFetchOKReturnsRawDocument(t *testing.T) {
	g := newGateway(protocol.StatusOK, "# body", map[string]string{"title": "T"})
	raw, err := g.Fetch(t.Context(), "soul.demarkus.io", "/x.md")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if raw.Body != "# body" || raw.Metadata["title"] != "T" {
		t.Errorf("unexpected raw doc: %+v", raw)
	}
	if raw.Source != "soul.demarkus.io:6309" {
		t.Errorf("source = %q, want default port appended", raw.Source)
	}
}

func TestFetchStatusMapping(t *testing.T) {
	cases := []struct {
		status string
		want   error
	}{
		{protocol.StatusNotFound, domain.ErrNotFound},
		{protocol.StatusArchived, domain.ErrArchived},
		{protocol.StatusUnauthorized, domain.ErrUnauthorized},
		{protocol.StatusNotPermitted, domain.ErrUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			g := newGateway(tc.status, "", nil)
			if _, err := g.Fetch(t.Context(), "soul.demarkus.io", "/x.md"); !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}

	// Archived stays absent for every caller that only asks that much, and
	// not-found must never pass for archived.
	t.Run("archived is also absent", func(t *testing.T) {
		g := newGateway(protocol.StatusArchived, "", nil)
		if _, err := g.Fetch(t.Context(), "soul.demarkus.io", "/x.md"); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("archived must still satisfy ErrNotFound, got %v", err)
		}
	})
	t.Run("missing is not archived", func(t *testing.T) {
		g := newGateway(protocol.StatusNotFound, "", nil)
		if _, err := g.Fetch(t.Context(), "soul.demarkus.io", "/x.md"); errors.Is(err, domain.ErrArchived) {
			t.Error("a missing document must not report as archived")
		}
	})
}

func TestFetchPropagatesTransportError(t *testing.T) {
	boom := errors.New("dial failed")
	g := NewGateway(fakeClient{err: boom}, "host", "")
	if _, err := g.Fetch(t.Context(), "soul.demarkus.io", "/x.md"); !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
}

func TestLookupAllUsesHomeWorld(t *testing.T) {
	g := newGateway(protocol.StatusOK, "| Path | Importance | Title | Tags |", nil)
	raw, err := g.LookupAll(t.Context(), domain.LookupQuery{Scope: "/", Query: "*", Limit: 1000})
	if err != nil {
		t.Fatalf("LookupAll: %v", err)
	}
	if raw.Source != "soul.demarkus.io:6309" || raw.Path != "/" {
		t.Errorf("raw = %+v", raw)
	}

	homeless := NewGateway(fakeClient{}, "", "")
	if _, err := homeless.LookupAll(t.Context(), domain.LookupQuery{Scope: "/", Query: "*", Limit: 1000}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("homeless LookupAll error = %v, want ErrNotFound", err)
	}
}

func TestNormalizeHost(t *testing.T) {
	cases := []struct{ in, want string }{
		{"soul.demarkus.io", "soul.demarkus.io:6309"}, // bare host → default port
		{"host:1234", "host:1234"},                    // explicit port preserved
		{"2001:db8::1", "[2001:db8::1]:6309"},         // bare IPv6 → bracketed + default port
		{"[2001:db8::1]:80", "[2001:db8::1]:80"},      // bracketed IPv6 with port preserved
		{"", ""},                                      // empty unchanged
	}
	for _, tc := range cases {
		if got := NormalizeHost(tc.in); got != tc.want {
			t.Errorf("NormalizeHost(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// recordingClient captures the host and token of each dial.
type recordingClient struct {
	fakeClient
	gotHost, gotToken string
}

func (r *recordingClient) Fetch(host, _, token string) (fetch.Result, error) {
	r.gotHost, r.gotToken = host, token
	return r.res, r.err
}

// TestTokenScopedToHomeHost pins the federation credential rail: the read
// token goes to the home host and ONLY the home host — following a link to
// another server must not leak it.
func TestTokenScopedToHomeHost(t *testing.T) {
	rc := &recordingClient{fakeClient: fakeClient{res: fetch.Result{Response: protocol.Response{Status: protocol.StatusOK}}}}
	g := NewGateway(rc, "soul.demarkus.io", "secret-token")

	// Home world (bare host normalizes to host:port) carries the token.
	if _, err := g.Fetch(t.Context(), "soul.demarkus.io", "/x.md"); err != nil {
		t.Fatalf("home Fetch: %v", err)
	}
	if rc.gotToken != "secret-token" {
		t.Errorf("home dial token = %q, want secret-token", rc.gotToken)
	}
	if rc.gotHost != "soul.demarkus.io:6309" {
		t.Errorf("home dial host = %q, want normalized", rc.gotHost)
	}

	// External world: anonymous, no credential leak.
	if _, err := g.Fetch(t.Context(), "wiki.example.org:6310", "/x.md"); err != nil {
		t.Fatalf("external Fetch: %v", err)
	}
	if rc.gotToken != "" {
		t.Errorf("external dial token = %q, want empty (credential must not leak)", rc.gotToken)
	}
	if rc.gotHost != "wiki.example.org:6310" {
		t.Errorf("external dial host = %q", rc.gotHost)
	}

	// A homeless gateway (broker-mode federation duty) never sends a token.
	g = NewGateway(rc, "", "ignored")
	if _, err := g.Fetch(t.Context(), "soul.demarkus.io", "/x.md"); err != nil {
		t.Fatalf("homeless Fetch: %v", err)
	}
	if rc.gotToken != "" {
		t.Errorf("homeless dial token = %q, want empty", rc.gotToken)
	}
}
