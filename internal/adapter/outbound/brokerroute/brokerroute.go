// Package brokerroute sends the library's server-side broker calls to an
// in-cluster address while keeping the broker's public identity: the URL
// host, Host header, and every discovery value stay public.
package brokerroute

import (
	"errors"
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

	mu    sync.RWMutex
	hosts map[string]struct{}
}

// ParseInternalURL validates an origin-only http(s) URL. Errors never echo
// the value: it may carry userinfo and the caller logs them at startup.
func ParseInternalURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("internal broker URL must be http(s)://host[:port]")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") != "" {
		return nil, errors.New("internal broker URL must be an origin: no userinfo, path, query or fragment")
	}
	return &url.URL{Scheme: u.Scheme, Host: u.Host}, nil
}

// New routes publicURL's host to internal over http.DefaultTransport.
func New(internal *url.URL, publicURL string) (*Transport, error) {
	public, err := url.Parse(publicURL)
	if err != nil || public.Host == "" {
		return nil, fmt.Errorf("invalid broker URL %q", publicURL)
	}
	t := &Transport{internal: internal, hosts: map[string]struct{}{}}
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
		return http.DefaultTransport.RoundTrip(req)
	}
	// Shallow copy: only URL and Host change, headers are shared untouched.
	out := *req
	target := *req.URL
	target.Scheme, target.Host = t.internal.Scheme, t.internal.Host
	out.URL, out.Host = &target, req.URL.Host
	return http.DefaultTransport.RoundTrip(&out)
}
