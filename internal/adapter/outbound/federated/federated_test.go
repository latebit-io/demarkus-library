package federated

import (
	"context"
	"errors"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// fakeGW records which route was taken. via, when set, captures the gateway
// name a write (Append) was routed to — writes return no Source to inspect.
type fakeGW struct {
	name string
	via  *string
}

func (f *fakeGW) Fetch(_ context.Context, _, path string) (domain.RawDocument, error) {
	return domain.RawDocument{Source: f.name, Path: path}, nil
}
func (f *fakeGW) List(_ context.Context, _, _ string) (domain.RawDocument, error) {
	return domain.RawDocument{Source: f.name}, nil
}
func (f *fakeGW) Versions(_ context.Context, _, _ string) (domain.RawDocument, error) {
	return domain.RawDocument{Source: f.name}, nil
}
func (f *fakeGW) Lookup(_ context.Context, _, _, _, _ string, _ int) (domain.RawDocument, error) {
	return domain.RawDocument{Source: f.name}, nil
}

func (f *fakeGW) Worlds(context.Context) ([]domain.WorldInfo, error) {
	return []domain.WorldInfo{{Name: f.name}}, nil
}
func (f *fakeGW) Publish(_ context.Context, _, _, _ string, _ domain.PublishMeta, _ int) (domain.PublishResult, error) {
	return domain.PublishResult{}, nil
}
func (f *fakeGW) Append(_ context.Context, _, _, _ string) (int, error) {
	if f.via != nil {
		*f.via = f.name
	}
	return 0, nil
}

func TestRouting(t *testing.T) {
	names := &fakeGW{name: "names"}
	hosts := &fakeGW{name: "hosts"}

	cases := []struct {
		name    string
		cfg     Config
		world   string
		wantVia string // "" = expect ErrNotFound
	}{
		// Broker mode with federation: names by shape, hosts by shape.
		{"name routes to broker", Config{Names: names, Hosts: hosts, AllowExternal: true}, "root", "names"},
		{"host routes direct", Config{Names: names, Hosts: hosts, AllowExternal: true}, "wiki.example.org", "hosts"},
		{"host:port routes direct", Config{Names: names, Hosts: hosts, AllowExternal: true}, "wiki.example.org:6310", "hosts"},

		// Broker mode, federation off: hosts unreachable.
		{"federation off blocks hosts", Config{Names: names, AllowExternal: false}, "wiki.example.org", ""},
		{"federation off keeps names", Config{Names: names, AllowExternal: false}, "root", "names"},

		// QUIC mode: no name resolver; home always allowed; external by flag.
		{"quic home allowed", Config{Hosts: hosts, HomeHost: "soul.demarkus.io", AllowExternal: false}, "soul.demarkus.io", "hosts"},
		{"quic home normalized port", Config{Hosts: hosts, HomeHost: "soul.demarkus.io", AllowExternal: false}, "soul.demarkus.io:6309", "hosts"},
		{"quic external blocked", Config{Hosts: hosts, HomeHost: "soul.demarkus.io", AllowExternal: false}, "wiki.example.org", ""},
		{"quic external allowed", Config{Hosts: hosts, HomeHost: "soul.demarkus.io", AllowExternal: true}, "wiki.example.org", "hosts"},
		{"quic name unroutable", Config{Hosts: hosts, HomeHost: "soul.demarkus.io", AllowExternal: true}, "root", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := New(tc.cfg)
			raw, err := g.Fetch(t.Context(), tc.world, "/x.md")
			if tc.wantVia == "" {
				if !errors.Is(err, domain.ErrNotFound) {
					t.Fatalf("err = %v, want ErrNotFound", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if raw.Source != tc.wantVia {
				t.Errorf("routed via %q, want %q", raw.Source, tc.wantVia)
			}
		})
	}
}

// TestAppendRouting pins the write path's routing against the same matrix as
// Fetch: names → broker, hosts → direct, blocked → ErrNotFound.
func TestAppendRouting(t *testing.T) {
	cases := []struct {
		name    string
		cfg     func(via *string) Config
		world   string
		wantVia string // "" = expect ErrNotFound
	}{
		{"name routes to broker", func(v *string) Config {
			return Config{Names: &fakeGW{name: "names", via: v}, Hosts: &fakeGW{name: "hosts", via: v}, AllowExternal: true}
		}, "root", "names"},
		{"host routes direct", func(v *string) Config {
			return Config{Names: &fakeGW{name: "names", via: v}, Hosts: &fakeGW{name: "hosts", via: v}, AllowExternal: true}
		}, "wiki.example.org", "hosts"},
		{"federation off blocks hosts", func(v *string) Config {
			return Config{Names: &fakeGW{name: "names", via: v}, AllowExternal: false}
		}, "wiki.example.org", ""},
		{"quic name unroutable", func(v *string) Config {
			return Config{Hosts: &fakeGW{name: "hosts", via: v}, HomeHost: "soul.demarkus.io", AllowExternal: true}
		}, "root", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var via string
			g := New(tc.cfg(&via))
			_, err := g.Append(t.Context(), tc.world, "/x.md", "more")
			if tc.wantVia == "" {
				if !errors.Is(err, domain.ErrNotFound) {
					t.Fatalf("err = %v, want ErrNotFound", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Append: %v", err)
			}
			if via != tc.wantVia {
				t.Errorf("routed via %q, want %q", via, tc.wantVia)
			}
		})
	}
}

func TestIsHostShaped(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"root", false},
		{"team-a", false},
		{"soul.demarkus.io", true},
		{"soul.demarkus.io:6309", true},
		{"localhost:6309", true},
		{"localhost", true},
		{"2001:db8::1", true},
	}
	for _, tc := range cases {
		if got := IsHostShaped(tc.in); got != tc.want {
			t.Errorf("IsHostShaped(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
