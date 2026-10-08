// Package brokerroute sends the library's server-side broker calls to an
// in-cluster address while keeping the broker's public identity: the URL
// host, Host header, and every discovery value stay public.
package brokerroute

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// Transport rewrites requests for known public broker hosts to the internal
// origin. Matching is exact host:port, never a wildcard, so a hostile
// discovery document cannot pull credentials toward the internal address.
type Transport struct {
	internal *url.URL
	next     http.RoundTripper

	mu    sync.RWMutex
	hosts map[string]struct{}
}

// ParseInternalURL validates an origin-only http(s) URL.
func ParseInternalURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("invalid internal broker URL %q: want http(s)://host[:port]", raw)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") != "" {
		return nil, fmt.Errorf("internal broker URL %q must be an origin: no userinfo, path, query or fragment", raw)
	}
	return &url.URL{Scheme: u.Scheme, Host: u.Host}, nil
}

// New routes publicURL's host to internal. A nil next uses http.DefaultTransport.
func New(internal *url.URL, publicURL string, next http.RoundTripper) (*Transport, error) {
	public, err := url.Parse(publicURL)
	if err != nil || public.Host == "" {
		return nil, fmt.Errorf("invalid broker URL %q", publicURL)
	}
	if next == nil {
		next = http.DefaultTransport
	}
	t := &Transport{internal: internal, next: next, hosts: map[string]struct{}{}}
	t.AddHost(public.Host)
	return t, nil
}

// AddHost routes one more public host (the discovered issuer) internally.
func (t *Transport) AddHost(host string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.hosts[strings.ToLower(host)] = struct{}{}
}

func (t *Transport) routed(host string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	_, ok := t.hosts[strings.ToLower(host)]
	return ok
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !t.routed(req.URL.Host) {
		return t.next.RoundTrip(req)
	}
	out := req.Clone(req.Context())
	out.Host = req.URL.Host
	out.URL.Scheme = t.internal.Scheme
	out.URL.Host = t.internal.Host
	return t.next.RoundTrip(out)
}
