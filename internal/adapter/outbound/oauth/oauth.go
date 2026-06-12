// Package oauth is the outbound adapter that speaks the broker's OAuth
// surface: discovery, the authorization-code redirect (PKCE S256), the token
// endpoint (exchange + refresh), and revocation. The library is a registered
// confidential web client at the broker (ADR 0004): every token-endpoint call
// authenticates with the client secret over HTTP Basic, and PKCE is still
// sent as defense in depth — the broker requires both.
//
// The package knows nothing of cookies or sessions; it turns broker HTTP
// exchanges into token sets and sentinel errors. Session lifecycle lives in
// the session package, wired together at the composition root.
package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ErrInvalidGrant means the broker rejected the code or refresh token itself
// (expired, revoked, consumed, PKCE mismatch). The credential is dead; the
// only recovery is a fresh login.
var ErrInvalidGrant = errors.New("oauth: grant rejected by broker")

// ErrInvalidClient means the broker rejected the client credentials — a
// deployment misconfiguration (wrong secret, deregistered client), not a user
// problem. Surfaced loudly rather than as a login redirect.
var ErrInvalidClient = errors.New("oauth: client authentication rejected by broker")

// discoveryTTL caches the broker's authorization-server metadata. Matches the
// broker's own 5-minute Cache-Control on the discovery document.
const discoveryTTL = 5 * time.Minute

// revokePath is the broker's RFC 7009 revocation endpoint. Not advertised in
// the discovery document, so the path is fixed here.
const revokePath = "/token/revoke"

// Config identifies this deployment as one registered confidential web client
// at the broker (the operator-curated webClients registry entry).
type Config struct {
	// BrokerURL is the broker origin, e.g. https://broker.example.org.
	BrokerURL string
	// ClientID matches the broker registry entry.
	ClientID string
	// ClientSecret is the plaintext secret whose sha256 the broker stores.
	ClientSecret string
	// RedirectURI must byte-for-byte match one registered redirect URI —
	// the broker compares exactly, no normalization.
	RedirectURI string
	// Scopes are space-joined into the authorize request. Default mark.read.
	Scopes []string
}

// TokenSet is one successful token-endpoint response. RefreshToken is empty
// on refresh responses — the broker does not rotate refresh tokens — and the
// caller keeps the one it has.
type TokenSet struct {
	IDToken      string
	RefreshToken string
	// Expiry is the id_token expiry instant, computed from expires_in at
	// receive time.
	Expiry time.Time
}

// endpoints is the slice of the discovery document this client uses.
type endpoints struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
}

// Client speaks to one broker as one registered web client. Safe for
// concurrent use; the discovery cache is the only mutable state.
type Client struct {
	cfg  Config
	http *http.Client
	now  func() time.Time

	mu        sync.Mutex
	cached    endpoints
	fetchedAt time.Time
}

// NewClient binds a Config to an HTTP client. A nil httpClient uses a
// 10-second-timeout default — every broker call is a small JSON exchange.
func NewClient(cfg Config, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{cfg: cfg, http: httpClient, now: time.Now}
}

// endpoints returns the broker's advertised endpoints, fetching the discovery
// document at most once per discoveryTTL. The lock is held across the fetch
// so concurrent callers on a cold cache produce one request, not a stampede.
func (c *Client) endpoints(ctx context.Context) (endpoints, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cached.TokenEndpoint != "" && c.now().Sub(c.fetchedAt) < discoveryTTL {
		return c.cached, nil
	}

	u := strings.TrimRight(c.cfg.BrokerURL, "/") + "/.well-known/oauth-authorization-server"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return endpoints{}, fmt.Errorf("oauth: build discovery request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return endpoints{}, fmt.Errorf("oauth: fetch discovery: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return endpoints{}, fmt.Errorf("oauth: discovery returned %s", resp.Status)
	}
	var eps endpoints
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&eps); err != nil {
		return endpoints{}, fmt.Errorf("oauth: decode discovery: %w", err)
	}
	if eps.AuthorizationEndpoint == "" || eps.TokenEndpoint == "" {
		return endpoints{}, errors.New("oauth: discovery document missing endpoints")
	}
	c.cached, c.fetchedAt = eps, c.now()
	return eps, nil
}

// AuthCodeURL builds the /oauth/authorize redirect for one login attempt.
// state and challenge come from GenerateState / Challenge(verifier); the
// caller stashes state+verifier server-side until the callback returns.
func (c *Client) AuthCodeURL(ctx context.Context, state, challenge string) (string, error) {
	eps, err := c.endpoints(ctx)
	if err != nil {
		return "", err
	}
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {c.cfg.ClientID},
		"redirect_uri":          {c.cfg.RedirectURI},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	if len(c.cfg.Scopes) > 0 {
		q.Set("scope", strings.Join(c.cfg.Scopes, " "))
	}
	return eps.AuthorizationEndpoint + "?" + q.Encode(), nil
}

// Exchange redeems the authorization code from the callback. verifier is the
// PKCE verifier whose challenge went into AuthCodeURL.
func (c *Client) Exchange(ctx context.Context, code, verifier string) (TokenSet, error) {
	return c.token(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {c.cfg.RedirectURI},
		"code_verifier": {verifier},
	})
}

// Refresh mints a fresh id_token from a refresh token. The returned set has
// an empty RefreshToken — the broker does not rotate; keep the one you have.
// ErrInvalidGrant means the refresh token is dead and the session with it.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (TokenSet, error) {
	return c.token(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	})
}

// Revoke drops a refresh token at the broker (RFC 7009, idempotent — an
// already-dead token is success). Called on logout.
func (c *Client) Revoke(ctx context.Context, refreshToken string) error {
	form := url.Values{"token": {refreshToken}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(c.cfg.BrokerURL, "/")+revokePath,
		strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("oauth: build revoke request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.setBasicAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("oauth: revoke: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("oauth: revoke returned %s", resp.Status)
	}
	return nil
}

// token POSTs one grant to the token endpoint with client authentication and
// maps the response onto TokenSet / sentinel errors.
func (c *Client) token(ctx context.Context, form url.Values) (TokenSet, error) {
	eps, err := c.endpoints(ctx)
	if err != nil {
		return TokenSet{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, eps.TokenEndpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return TokenSet{}, fmt.Errorf("oauth: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.setBasicAuth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return TokenSet{}, fmt.Errorf("oauth: token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return TokenSet{}, fmt.Errorf("oauth: read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var oe struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &oe) // best effort; fall through to status text
		switch oe.Error {
		case "invalid_grant":
			return TokenSet{}, ErrInvalidGrant
		case "invalid_client":
			return TokenSet{}, ErrInvalidClient
		case "":
			return TokenSet{}, fmt.Errorf("oauth: token endpoint returned %s", resp.Status)
		default:
			return TokenSet{}, fmt.Errorf("oauth: token endpoint returned %s (%s)", oe.Error, resp.Status)
		}
	}

	var ts struct {
		IDToken      string `json:"id_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &ts); err != nil {
		return TokenSet{}, fmt.Errorf("oauth: decode token response: %w", err)
	}
	if ts.IDToken == "" {
		return TokenSet{}, errors.New("oauth: token response missing id_token")
	}
	return TokenSet{
		IDToken:      ts.IDToken,
		RefreshToken: ts.RefreshToken,
		Expiry:       c.now().Add(time.Duration(ts.ExpiresIn) * time.Second),
	}, nil
}

// setBasicAuth attaches the client credentials per RFC 6749 §2.3.1: both
// halves are form-urlencoded BEFORE Basic encoding, and the broker
// query-unescapes them after decoding. Plain SetBasicAuth on a secret
// containing ':' or '%' would desync from the broker's parse.
func (c *Client) setBasicAuth(req *http.Request) {
	req.SetBasicAuth(url.QueryEscape(c.cfg.ClientID), url.QueryEscape(c.cfg.ClientSecret))
}
