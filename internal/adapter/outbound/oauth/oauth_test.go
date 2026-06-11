package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// secret deliberately contains ':' and '%' — the characters that break naive
// Basic auth — to pin the RFC 6749 §2.3.1 form-urlencode-then-Basic rule.
const (
	testClientID = "library-web"
	testSecret   = "s3cret:with%funny/chars"
	testRedirect = "https://library.example.org/auth/callback"
)

// brokerStub fakes the two broker endpoints the client touches, recording
// the last token-endpoint form and credentials it saw.
type brokerStub struct {
	srv            *httptest.Server
	discoveryHits  atomic.Int64
	lastForm       url.Values
	lastBasicID    string
	lastBasicSec   string
	tokenResponses []tokenResponse // consumed in order; last one repeats
	tokenCalls     int
}

type tokenResponse struct {
	status int
	body   string
}

func newBrokerStub(t *testing.T) *brokerStub {
	t.Helper()
	b := &brokerStub{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		b.discoveryHits.Add(1)
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 b.srv.URL,
			"authorization_endpoint": b.srv.URL + "/oauth/authorize",
			"token_endpoint":         b.srv.URL + "/device/token",
		})
	})
	mux.HandleFunc("POST /device/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("token endpoint: parse form: %v", err)
		}
		b.lastForm = r.PostForm
		// Mirror the broker's parseClientAuth: Basic halves are
		// form-urlencoded before encoding.
		if id, sec, ok := r.BasicAuth(); ok {
			b.lastBasicID, _ = url.QueryUnescape(id)
			b.lastBasicSec, _ = url.QueryUnescape(sec)
		}
		resp := b.tokenResponses[min(b.tokenCalls, len(b.tokenResponses)-1)]
		b.tokenCalls++
		w.WriteHeader(resp.status)
		w.Write([]byte(resp.body))
	})
	mux.HandleFunc("POST /token/revoke", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		b.lastForm = r.PostForm
		w.WriteHeader(http.StatusNoContent)
	})
	b.srv = httptest.NewServer(mux)
	t.Cleanup(b.srv.Close)
	return b
}

func (b *brokerStub) client() *Client {
	return NewClient(Config{
		BrokerURL:    b.srv.URL,
		ClientID:     testClientID,
		ClientSecret: testSecret,
		RedirectURI:  testRedirect,
		Scopes:       []string{"mark.read"},
	}, nil)
}

func okToken(idToken, refresh string, expiresIn int) tokenResponse {
	body, _ := json.Marshal(map[string]any{
		"access_token":  idToken,
		"id_token":      idToken,
		"refresh_token": refresh,
		"token_type":    "Bearer",
		"expires_in":    expiresIn,
	})
	return tokenResponse{status: http.StatusOK, body: string(body)}
}

func TestAuthCodeURL(t *testing.T) {
	b := newBrokerStub(t)
	c := b.client()

	raw, err := c.AuthCodeURL(context.Background(), "the-state", "the-challenge")
	if err != nil {
		t.Fatalf("AuthCodeURL: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse authorize URL: %v", err)
	}
	if u.Path != "/oauth/authorize" {
		t.Errorf("path = %q, want /oauth/authorize", u.Path)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"response_type":         "code",
		"client_id":             testClientID,
		"redirect_uri":          testRedirect,
		"state":                 "the-state",
		"code_challenge":        "the-challenge",
		"code_challenge_method": "S256",
		"scope":                 "mark.read",
	} {
		if got := q.Get(k); got != want {
			t.Errorf("authorize param %s = %q, want %q", k, got, want)
		}
	}
}

func TestDiscoveryCached(t *testing.T) {
	b := newBrokerStub(t)
	c := b.client()
	ctx := context.Background()

	if _, err := c.AuthCodeURL(ctx, "s", "c"); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if _, err := c.AuthCodeURL(ctx, "s", "c"); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if hits := b.discoveryHits.Load(); hits != 1 {
		t.Errorf("discovery fetched %d times, want 1 (cached)", hits)
	}
}

func TestExchange(t *testing.T) {
	b := newBrokerStub(t)
	b.tokenResponses = []tokenResponse{okToken("id-jwt", "refresh-1", 300)}
	c := b.client()

	before := time.Now()
	ts, err := c.Exchange(context.Background(), "the-code", "the-verifier")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if ts.IDToken != "id-jwt" || ts.RefreshToken != "refresh-1" {
		t.Errorf("TokenSet = %+v", ts)
	}
	wantExpiry := before.Add(300 * time.Second)
	if ts.Expiry.Before(wantExpiry) || ts.Expiry.After(wantExpiry.Add(5*time.Second)) {
		t.Errorf("Expiry = %v, want ~%v", ts.Expiry, wantExpiry)
	}

	for k, want := range map[string]string{
		"grant_type":    "authorization_code",
		"code":          "the-code",
		"redirect_uri":  testRedirect,
		"code_verifier": "the-verifier",
	} {
		if got := b.lastForm.Get(k); got != want {
			t.Errorf("token form %s = %q, want %q", k, got, want)
		}
	}
	// The broker must see the original (unescaped) credentials after its
	// QueryUnescape — pins the §2.3.1 escaping round trip.
	if b.lastBasicID != testClientID || b.lastBasicSec != testSecret {
		t.Errorf("broker saw creds %q / %q, want %q / %q",
			b.lastBasicID, b.lastBasicSec, testClientID, testSecret)
	}
	if b.lastForm.Get("client_secret") != "" {
		t.Error("client_secret leaked into form alongside Basic auth")
	}
}

func TestRefreshKeepsNoRotation(t *testing.T) {
	b := newBrokerStub(t)
	// Broker refresh responses omit refresh_token (no rotation).
	body, _ := json.Marshal(map[string]any{
		"access_token": "new-id",
		"id_token":     "new-id",
		"token_type":   "Bearer",
		"expires_in":   300,
	})
	b.tokenResponses = []tokenResponse{{status: http.StatusOK, body: string(body)}}
	c := b.client()

	ts, err := c.Refresh(context.Background(), "refresh-1")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if ts.IDToken != "new-id" {
		t.Errorf("IDToken = %q", ts.IDToken)
	}
	if ts.RefreshToken != "" {
		t.Errorf("RefreshToken = %q, want empty (broker does not rotate)", ts.RefreshToken)
	}
	if got := b.lastForm.Get("grant_type"); got != "refresh_token" {
		t.Errorf("grant_type = %q", got)
	}
	if got := b.lastForm.Get("refresh_token"); got != "refresh-1" {
		t.Errorf("refresh_token = %q", got)
	}
}

func TestTokenErrorMapping(t *testing.T) {
	cases := []struct {
		name    string
		resp    tokenResponse
		wantErr error
	}{
		{"invalid_grant", tokenResponse{http.StatusBadRequest, `{"error":"invalid_grant"}`}, ErrInvalidGrant},
		{"invalid_client", tokenResponse{http.StatusUnauthorized, `{"error":"invalid_client"}`}, ErrInvalidClient},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newBrokerStub(t)
			b.tokenResponses = []tokenResponse{tc.resp}
			_, err := b.client().Refresh(context.Background(), "dead")
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}

	t.Run("other_error", func(t *testing.T) {
		b := newBrokerStub(t)
		b.tokenResponses = []tokenResponse{{http.StatusBadRequest, `{"error":"unsupported_grant_type"}`}}
		_, err := b.client().Refresh(context.Background(), "x")
		if err == nil || errors.Is(err, ErrInvalidGrant) || errors.Is(err, ErrInvalidClient) {
			t.Errorf("err = %v, want generic error", err)
		}
		if !strings.Contains(err.Error(), "unsupported_grant_type") {
			t.Errorf("err %v should name the broker error code", err)
		}
	})
}

func TestRevoke(t *testing.T) {
	b := newBrokerStub(t)
	c := b.client()

	if err := c.Revoke(context.Background(), "refresh-1"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if got := b.lastForm.Get("token"); got != "refresh-1" {
		t.Errorf("revoke form token = %q", got)
	}
}

func TestPKCE(t *testing.T) {
	// RFC 7636 appendix B test vector.
	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	const challenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if got := Challenge(verifier); got != challenge {
		t.Errorf("Challenge = %q, want %q", got, challenge)
	}

	v, v2 := GenerateVerifier(), GenerateVerifier()
	if len(v) != 43 {
		t.Errorf("verifier length = %d, want 43", len(v))
	}
	if v == v2 {
		t.Error("two verifiers identical — randomness broken")
	}
	s1, s2 := GenerateState(), GenerateState()
	if s1 == s2 {
		t.Error("two states identical — randomness broken")
	}
}
