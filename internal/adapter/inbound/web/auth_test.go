package web

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/adapter/bearer"
	"github.com/latebit-io/demarkus-library/internal/adapter/inbound/web/session"
)

// fakeAuthority satisfies session.Authority for manager construction; the
// middleware tests never hit refresh (tokens are minted fresh).
type fakeAuthority struct {
	refreshErr error
	revoked    []string
}

func (f *fakeAuthority) Refresh(_ context.Context, _ string) (session.Tokens, error) {
	if f.refreshErr != nil {
		return session.Tokens{}, f.refreshErr
	}
	return session.Tokens{IDToken: "refreshed", Expiry: time.Now().Add(time.Hour)}, nil
}

func (f *fakeAuthority) Revoke(_ context.Context, token string) error {
	f.revoked = append(f.revoked, token)
	return nil
}

// fakeFlow scripts the LoginFlow for handler tests.
type fakeFlow struct {
	authURL     string
	state       string
	verifier    string
	beginErr    error
	tokens      session.Tokens
	exchangeErr error

	gotCode     string
	gotVerifier string
}

func (f *fakeFlow) Begin(_ context.Context) (string, string, string, error) {
	if f.beginErr != nil {
		return "", "", "", f.beginErr
	}
	return f.authURL, f.state, f.verifier, nil
}

func (f *fakeFlow) Exchange(_ context.Context, code, verifier string) (session.Tokens, error) {
	f.gotCode, f.gotVerifier = code, verifier
	if f.exchangeErr != nil {
		return session.Tokens{}, f.exchangeErr
	}
	return f.tokens, nil
}

// testRig assembles an Echo app with the auth surface and a turnstiled probe
// route that records the bearer it saw.
type testRig struct {
	app      *echo.Echo
	sessions *session.Manager
	pending  *session.PendingStore
	auth     *fakeAuthority
	flow     *fakeFlow

	probeBearer string
}

func newTestRig(t *testing.T) *testRig {
	t.Helper()
	rig := &testRig{
		auth: &fakeAuthority{},
		flow: &fakeFlow{authURL: "https://broker.example.org/oauth/authorize?x=1", state: "st-1", verifier: "ver-1"},
	}
	store := session.NewMemoryStore(time.Hour)
	rig.sessions = session.NewManager(store, rig.auth, 0)
	rig.pending = session.NewPendingStore()

	rig.app = echo.New()
	view, err := NewView()
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}
	rig.app.Renderer = view

	AuthRoutes(rig.app, NewAuthHandler(rig.flow, rig.sessions, rig.pending, CookieConfig{TTL: time.Hour}))
	rig.app.GET("/probe", func(c *echo.Context) error {
		rig.probeBearer = bearer.FromContext(c.Request().Context())
		return c.String(http.StatusOK, "ok")
	}, RequireSession(rig.sessions))
	return rig
}

func (r *testRig) do(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r.app.ServeHTTP(rec, req)
	return rec
}

// login walks the full happy path and returns the session cookie.
func (r *testRig) login(t *testing.T) *http.Cookie {
	t.Helper()
	r.flow.tokens = session.Tokens{IDToken: "id-tok", RefreshToken: "ref-tok", Expiry: time.Now().Add(time.Hour)}

	form := url.Values{"return_to": {"/d/index.md"}}
	req := httptest.NewRequest(http.MethodPost, "/login/start", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if rec := r.do(req); rec.Code != http.StatusSeeOther {
		t.Fatalf("start: code = %d", rec.Code)
	}

	rec := r.do(httptest.NewRequest(http.MethodGet, "/auth/callback?state=st-1&code=the-code", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("callback: code = %d, body %s", rec.Code, rec.Body.String())
	}
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == sessionCookie && ck.Value != "" {
			return ck
		}
	}
	t.Fatal("callback set no session cookie")
	return nil
}

func TestTurnstileRedirectsAnonymous(t *testing.T) {
	rig := newTestRig(t)

	rec := rig.do(httptest.NewRequest(http.MethodGet, "/probe?x=1", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("code = %d, want 302", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/login?return_to=") {
		t.Errorf("Location = %q", loc)
	}
	if !strings.Contains(loc, url.QueryEscape("/probe?x=1")) {
		t.Errorf("return_to lost the original target: %q", loc)
	}
}

func TestTurnstileHTMXGetsHXRedirect(t *testing.T) {
	rig := newTestRig(t)

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("HX-Request", "true")
	rec := rig.do(req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("code = %d, want 401", rec.Code)
	}
	if got := rec.Header().Get("HX-Redirect"); !strings.HasPrefix(got, "/login") {
		t.Errorf("HX-Redirect = %q", got)
	}
}

func TestFullLoginFlow(t *testing.T) {
	rig := newTestRig(t)

	// Start redirects to the broker authorize URL.
	form := url.Values{"return_to": {"/d/index.md"}}
	req := httptest.NewRequest(http.MethodPost, "/login/start", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := rig.do(req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("start: code = %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != rig.flow.authURL {
		t.Errorf("start Location = %q", loc)
	}

	// Callback with the matching state mints the session and lands on
	// the stashed return_to.
	rig.flow.tokens = session.Tokens{IDToken: "id-tok", RefreshToken: "ref-tok", Expiry: time.Now().Add(time.Hour)}
	rec = rig.do(httptest.NewRequest(http.MethodGet, "/auth/callback?state=st-1&code=the-code", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("callback: code = %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/d/index.md" {
		t.Errorf("callback Location = %q, want /d/index.md", loc)
	}
	if rig.flow.gotCode != "the-code" || rig.flow.gotVerifier != "ver-1" {
		t.Errorf("exchange got code=%q verifier=%q", rig.flow.gotCode, rig.flow.gotVerifier)
	}

	var cookie *http.Cookie
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == sessionCookie {
			cookie = ck
		}
	}
	if cookie == nil || cookie.Value == "" {
		t.Fatal("no session cookie set")
	}
	if !cookie.HttpOnly {
		t.Error("session cookie must be HttpOnly")
	}

	// The turnstile now passes and the probe sees the bearer.
	req = httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.AddCookie(cookie)
	if rec := rig.do(req); rec.Code != http.StatusOK {
		t.Fatalf("probe after login: code = %d", rec.Code)
	}
	if rig.probeBearer != "id-tok" {
		t.Errorf("probe bearer = %q, want id-tok", rig.probeBearer)
	}
}

func TestCallbackRejectsBadState(t *testing.T) {
	rig := newTestRig(t)

	rec := rig.do(httptest.NewRequest(http.MethodGet, "/auth/callback?state=never-issued&code=x", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("code = %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login?err=failed" {
		t.Errorf("Location = %q", loc)
	}

	// State is single-use: a replay after a successful login also fails.
	rig.login(t)
	rec = rig.do(httptest.NewRequest(http.MethodGet, "/auth/callback?state=st-1&code=x", nil))
	if loc := rec.Header().Get("Location"); loc != "/login?err=failed" {
		t.Errorf("replayed state: Location = %q", loc)
	}
}

func TestCallbackDeniedAtIdP(t *testing.T) {
	rig := newTestRig(t)

	form := url.Values{}
	req := httptest.NewRequest(http.MethodPost, "/login/start", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rig.do(req)

	rec := rig.do(httptest.NewRequest(http.MethodGet, "/auth/callback?state=st-1&error=access_denied", nil))
	if loc := rec.Header().Get("Location"); loc != "/login?err=denied" {
		t.Errorf("Location = %q", loc)
	}
}

func TestLogout(t *testing.T) {
	rig := newTestRig(t)
	cookie := rig.login(t)

	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(cookie)
	rec := rig.do(req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("logout: code = %d", rec.Code)
	}
	if len(rig.auth.revoked) != 1 || rig.auth.revoked[0] != "ref-tok" {
		t.Errorf("revoked = %v, want [ref-tok]", rig.auth.revoked)
	}

	// The cookie is dead: the turnstile bounces the next request.
	req = httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.AddCookie(cookie)
	if rec := rig.do(req); rec.Code != http.StatusFound {
		t.Errorf("probe after logout: code = %d, want 302", rec.Code)
	}
}

func TestLoginPageRendersError(t *testing.T) {
	rig := newTestRig(t)

	rec := rig.do(httptest.NewRequest(http.MethodGet, "/login?err=denied", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), loginErrors["denied"]) {
		t.Error("login page missing the denied message")
	}

	// Unknown error codes render nothing — no reflected content channel.
	rec = rig.do(httptest.NewRequest(http.MethodGet, "/login?err=<script>alert(1)</script>", nil))
	if strings.Contains(rec.Body.String(), "script>") {
		t.Error("login page reflected the err parameter")
	}
}

func TestSanitizeReturnTo(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/d/index.md", "/d/index.md"},
		{"/search?q=hex", "/search?q=hex"},
		{"", ""},
		{"https://evil.example/", ""},
		{"//evil.example", ""},
		{"/d/x\\..", ""},
		{"javascript:alert(1)", ""},
		{"/d/a://b", ""},
	}
	for _, tc := range cases {
		if got := sanitizeReturnTo(tc.in); got != tc.want {
			t.Errorf("sanitizeReturnTo(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEmailFromIDToken(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"fritz@latebit.io","sub":"x"}`))
	if got := emailFromIDToken("h." + payload + ".sig"); got != "fritz@latebit.io" {
		t.Errorf("emailFromIDToken = %q, want fritz@latebit.io", got)
	}
	// Display-only: malformed or email-less tokens yield "" and never error.
	for _, bad := range []string{
		"",
		"not-a-jwt",
		"h.@@@bad-base64@@@.s",
		"h." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"x"}`)) + ".s",
	} {
		if got := emailFromIDToken(bad); got != "" {
			t.Errorf("emailFromIDToken(%q) = %q, want empty", bad, got)
		}
	}
}

func TestTurnstileTransientFailureIs502(t *testing.T) {
	rig := newTestRig(t)
	cookie := rig.login(t)

	// Make the next refresh transiently fail and force one by expiring the
	// id_token: log in again with an already-stale expiry.
	rig.auth.refreshErr = errors.New("connection refused")
	rig.flow.state = "st-2"
	rig.flow.tokens = session.Tokens{IDToken: "stale", RefreshToken: "r2", Expiry: time.Now().Add(-time.Minute)}
	form := url.Values{}
	req := httptest.NewRequest(http.MethodPost, "/login/start", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rig.do(req)
	rec := rig.do(httptest.NewRequest(http.MethodGet, "/auth/callback?state=st-2&code=c2", nil))
	var staleCookie *http.Cookie
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == sessionCookie {
			staleCookie = ck
		}
	}
	if staleCookie == nil {
		t.Fatal("no cookie from second login")
	}

	req = httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.AddCookie(staleCookie)
	if rec := rig.do(req); rec.Code != http.StatusBadGateway {
		t.Errorf("transient refresh failure: code = %d, want 502", rec.Code)
	}

	// The healthy session is untouched.
	req = httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.AddCookie(cookie)
	if rec := rig.do(req); rec.Code != http.StatusOK {
		t.Errorf("healthy session: code = %d, want 200", rec.Code)
	}
}
