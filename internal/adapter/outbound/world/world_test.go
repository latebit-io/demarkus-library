package world

import (
	"errors"
	"testing"

	"github.com/latebit/demarkus-library/internal/core/domain"
	"github.com/latebit/demarkus/client/fetch"
	"github.com/latebit/demarkus/protocol"
)

type fakeClient struct {
	res fetch.Result
	err error
}

func (f fakeClient) Fetch(string, string, string) (fetch.Result, error)    { return f.res, f.err }
func (f fakeClient) List(string, string, string) (fetch.Result, error)     { return f.res, f.err }
func (f fakeClient) Versions(string, string, string) (fetch.Result, error) { return f.res, f.err }
func (f fakeClient) Lookup(_, _, _, _ string, _ fetch.LookupOptions) (fetch.Result, error) {
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
	raw, err := g.Fetch(t.Context(), "/x.md")
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
		{protocol.StatusArchived, domain.ErrNotFound},
		{protocol.StatusUnauthorized, domain.ErrUnauthorized},
		{protocol.StatusNotPermitted, domain.ErrUnauthorized},
	}
	for _, tc := range cases {
		g := newGateway(tc.status, "", nil)
		if _, err := g.Fetch(t.Context(), "/x.md"); !errors.Is(err, tc.want) {
			t.Errorf("status %s: err = %v, want %v", tc.status, err, tc.want)
		}
	}
}

func TestFetchPropagatesTransportError(t *testing.T) {
	boom := errors.New("dial failed")
	g := NewGateway(fakeClient{err: boom}, "host", "")
	if _, err := g.Fetch(t.Context(), "/x.md"); !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
}

func TestWithDefaultPort(t *testing.T) {
	cases := []struct{ in, want string }{
		{"soul.demarkus.io", "soul.demarkus.io:6309"}, // bare host → default port
		{"host:1234", "host:1234"},                    // explicit port preserved
		{"2001:db8::1", "[2001:db8::1]:6309"},          // bare IPv6 → bracketed + default port
		{"[2001:db8::1]:80", "[2001:db8::1]:80"},       // bracketed IPv6 with port preserved
		{"", ""},                                       // empty unchanged
	}
	for _, tc := range cases {
		if got := withDefaultPort(tc.in); got != tc.want {
			t.Errorf("withDefaultPort(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
