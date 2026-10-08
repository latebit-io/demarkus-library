package brokerroute_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/latebit-io/demarkus-library/internal/adapter/outbound/brokerroute"
	"github.com/latebit-io/demarkus-library/internal/adapter/outbound/oauth"
)

const (
	publicBroker = "http://broker.public.test"
	publicIssuer = "http://auth.public.test"
)

// internalBroker serves PRM and RFC 8414 metadata under public identities and
// records the Host header of every request it receives.
func internalBroker(t *testing.T) (srv *httptest.Server, hosts *[]string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"authorization_servers": []string{publicIssuer}})
	})
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 publicIssuer,
			"authorization_endpoint": publicIssuer + "/oauth/authorize",
			"token_endpoint":         publicIssuer + "/oauth/token",
		})
	})
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Host)
		mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func newTransport(t *testing.T, internal string) *brokerroute.Transport {
	t.Helper()
	u, err := brokerroute.ParseInternalURL(internal)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := brokerroute.New(u, publicBroker, nil)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func TestOAuthDiscoveryUsesInternalAddress(t *testing.T) {
	srv, hosts := internalBroker(t)
	tr := newTransport(t, srv.URL)
	c := oauth.NewClient(oauth.Config{
		BrokerURL: publicBroker, ClientID: "id", RedirectURI: "http://lib/cb",
		OnIssuerHost: tr.AddHost,
	}, &http.Client{Transport: tr})

	got, err := c.AuthCodeURL(context.Background(), "st", "ch")
	if err != nil {
		t.Fatalf("AuthCodeURL with public hosts unreachable: %v", err)
	}
	// The browser still goes to the public authorize endpoint.
	if !strings.HasPrefix(got, publicIssuer+"/oauth/authorize?") {
		t.Errorf("authorize URL = %q, want public issuer", got)
	}
	want := []string{"broker.public.test", "auth.public.test"}
	if len(*hosts) != 2 || (*hosts)[0] != want[0] || (*hosts)[1] != want[1] {
		t.Errorf("Host headers = %v, want %v", *hosts, want)
	}
}

func TestOtherHostsNotRewritten(t *testing.T) {
	internal, _ := internalBroker(t)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(other.Close)

	client := &http.Client{Transport: newTransport(t, internal.URL)}
	resp, err := client.Get(other.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("status = %d, want request to reach the other host", resp.StatusCode)
	}
}

func TestUnknownIssuerHostStaysPublic(t *testing.T) {
	srv, hosts := internalBroker(t)
	client := &http.Client{Transport: newTransport(t, srv.URL)}
	// auth.public.test was never announced, so it must not reach the internal server.
	if _, err := client.Get(publicIssuer + "/x"); err == nil {
		t.Error("unannounced host was routed internally")
	}
	if len(*hosts) != 0 {
		t.Errorf("internal server saw %v, want none", *hosts)
	}
}

func TestParseInternalURL(t *testing.T) {
	for _, bad := range []string{"", "broker", "ftp://b", "http://u:p@b", "http://b/path", "http://b?q=1", "http://b#f"} {
		if _, err := brokerroute.ParseInternalURL(bad); err == nil {
			t.Errorf("ParseInternalURL(%q) accepted", bad)
		}
	}
	u, err := brokerroute.ParseInternalURL("http://broker.ns.svc.cluster.local:8080/")
	if err != nil {
		t.Fatal(err)
	}
	if (&url.URL{Scheme: "http", Host: "broker.ns.svc.cluster.local:8080"}).String() != u.String() {
		t.Errorf("got %s", u)
	}
}
