package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/adapter/bearer"
	"github.com/latebit-io/demarkus-library/internal/adapter/inbound/web/session"
	"github.com/latebit-io/demarkus-library/internal/core/port"
)

// fakeAuthority satisfies session.Authority: refresh mints "refreshed" until
// the refresh token is revoked, then answers like a broker with a dead grant.
type fakeAuthority struct {
	refreshErr error
	refreshes  int
	revoked    []string
}

func (f *fakeAuthority) Refresh(_ context.Context, refreshToken string) (session.Tokens, error) {
	f.refreshes++
	if f.refreshErr != nil {
		return session.Tokens{}, f.refreshErr
	}
	if slices.Contains(f.revoked, refreshToken) {
		return session.Tokens{}, session.ErrGrantDead
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

func (f *fakeFlow) Begin(_ context.Context) (LoginStart, error) {
	if f.beginErr != nil {
		return LoginStart{}, f.beginErr
	}
	return LoginStart{AuthURL: f.authURL, State: f.state, Verifier: f.verifier}, nil
}

func (f *fakeFlow) Exchange(_ context.Context, code, verifier string) (session.Tokens, error) {
	f.gotCode, f.gotVerifier = code, verifier
	if f.exchangeErr != nil {
		return session.Tokens{}, f.exchangeErr
	}
	return f.tokens, nil
}

// testSealingKey is shared by every rig, so two rigs act as two replicas.
var testSealingKey = bytes.Repeat([]byte{5}, session.KeySize)

// testRig assembles an Echo app with the auth surface and a turnstiled probe
// route that records the bearer it saw.
type testRig struct {
	app      *echo.Echo
	sessions *session.Manager
	cookies  CookieConfig
	auth     *fakeAuthority
	flow     *fakeFlow

	probeBearer string
	probeReader string
}

func newTestRig(t *testing.T) *testRig {
	t.Helper()
	rig := &testRig{
		auth: &fakeAuthority{},
		flow: &fakeFlow{
			authURL: "https://broker.example.org/oauth/authorize?x=1", state: "st-1", verifier: "ver-1",
			tokens: session.Tokens{IDToken: "id-tok", RefreshToken: "ref-tok", Expiry: time.Now().Add(time.Hour)},
		},
	}
	sealer, err := session.NewSealer(testSealingKey)
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	rig.sessions = session.NewManager(sealer, rig.auth, session.Config{TTL: time.Hour})
	rig.cookies = CookieConfig{}

	rig.app = echo.New()
	view, err := NewView()
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}
	rig.app.Renderer = view

	AuthRoutes(rig.app, NewAuthHandler(rig.flow, rig.sessions, session.NewPendingLogins(sealer), rig.cookies))
	rig.app.GET("/probe", func(c *echo.Context) error {
		rig.probeBearer = bearer.FromContext(c.Request().Context())
		rig.probeReader = port.ReaderKey(c.Request().Context())
		return c.String(http.StatusOK, "ok")
	}, RequireSession(rig.sessions, rig.cookies))
	return rig
}

func (r *testRig) do(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r.app.ServeHTTP(rec, req)
	return rec
}

// start posts /login/start and returns the login cookie it set.
func (r *testRig) start(t *testing.T, returnTo string) *http.Cookie {
	t.Helper()
	form := url.Values{"return_to": {returnTo}}
	req := httptest.NewRequest(http.MethodPost, "/login/start", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := r.do(req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("start: code = %d", rec.Code)
	}
	login := responseCookie(rec, loginCookie)
	if login == nil || login.Value == "" {
		t.Fatal("start set no login cookie")
	}
	return login
}

// callback lands the broker redirect, carrying login when non-nil.
func (r *testRig) callback(query string, login *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/auth/callback?"+query, http.NoBody)
	if login != nil {
		req.AddCookie(login)
	}
	return r.do(req)
}

// probe hits the turnstiled route with cookie.
func (r *testRig) probe(cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/probe", http.NoBody)
	req.AddCookie(cookie)
	return r.do(req)
}

// login walks the full happy path with tokens and returns the session cookie.
func (r *testRig) loginWith(t *testing.T, tokens session.Tokens) *http.Cookie {
	t.Helper()
	r.flow.tokens = tokens
	rec := r.callback("state="+r.flow.state+"&code=the-code", r.start(t, "/d/index.md"))
	if rec.Code != http.StatusFound {
		t.Fatalf("callback: code = %d, body %s", rec.Code, rec.Body.String())
	}
	ck := responseCookie(rec, sessionCookie)
	if ck == nil || ck.Value == "" {
		t.Fatal("callback set no session cookie")
	}
	return ck
}

func (r *testRig) login(t *testing.T) *http.Cookie {
	t.Helper()
	return r.loginWith(t, r.flow.tokens)
}

// responseCookie returns the named cookie the response set, or nil.
func responseCookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == name {
			return ck
		}
	}
	return nil
}

// cookieCleared reports whether the response expired the named cookie.
func cookieCleared(rec *httptest.ResponseRecorder, name string) bool {
	ck := responseCookie(rec, name)
	return ck != nil && ck.MaxAge < 0
}

func TestTurnstileRedirectsAnonymous(t *testing.T) {
	rig := newTestRig(t)

	rec := rig.do(httptest.NewRequest(http.MethodGet, "/probe?x=1", http.NoBody))
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

	req := httptest.NewRequest(http.MethodGet, "/probe", http.NoBody)
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

	// Start redirects to the broker authorize URL with the attempt sealed
	// into an HttpOnly cookie scoped to the callback.
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
	login := responseCookie(rec, loginCookie)
	if login == nil || !login.HttpOnly || login.Path != loginCookiePath {
		t.Fatalf("login cookie = %+v", login)
	}
	if strings.Contains(login.Value, "ver-1") {
		t.Error("login cookie exposes the PKCE verifier")
	}

	// Callback with the matching state mints the session and lands on
	// the stashed return_to.
	rec = rig.callback("state=st-1&code=the-code", login)
	if rec.Code != http.StatusFound {
		t.Fatalf("callback: code = %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/d/index.md" {
		t.Errorf("callback Location = %q, want /d/index.md", loc)
	}
	if rig.flow.gotCode != "the-code" || rig.flow.gotVerifier != "ver-1" {
		t.Errorf("exchange got code=%q verifier=%q", rig.flow.gotCode, rig.flow.gotVerifier)
	}

	cookie := responseCookie(rec, sessionCookie)
	if cookie == nil || cookie.Value == "" {
		t.Fatal("no session cookie set")
	}
	if !cookie.HttpOnly {
		t.Error("session cookie must be HttpOnly")
	}
	if strings.Contains(cookie.Value, "id-tok") || strings.Contains(cookie.Value, "ref-tok") {
		t.Error("session cookie carries tokens in the clear")
	}

	// The turnstile now passes and the probe sees the bearer.
	if rec := rig.probe(cookie); rec.Code != http.StatusOK {
		t.Fatalf("probe after login: code = %d", rec.Code)
	}
	if rig.probeBearer != "id-tok" {
		t.Errorf("probe bearer = %q, want id-tok", rig.probeBearer)
	}
	if rig.probeReader == "" {
		t.Error("probe reader key empty; the session id should mark the request")
	}
}

func TestCallbackRejectsBadState(t *testing.T) {
	rig := newTestRig(t)
	login := rig.start(t, "/")

	tests := []struct {
		name  string
		query string
		login *http.Cookie
	}{
		{name: "no login cookie", query: "state=st-1&code=x"},
		{name: "state never issued", query: "state=never-issued&code=x", login: login},
		{name: "forged login cookie", query: "state=st-1&code=x", login: &http.Cookie{Name: loginCookie, Value: "forged"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := rig.callback(tt.query, tt.login)
			if loc := rec.Header().Get("Location"); loc != "/login?err=failed" {
				t.Errorf("Location = %q", loc)
			}
			if responseCookie(rec, sessionCookie) != nil {
				t.Error("rejected callback set a session cookie")
			}
		})
	}
}

// TestCallbackSpendsLoginCookie: every callback clears the login cookie, so a
// browser cannot replay the attempt.
func TestCallbackSpendsLoginCookie(t *testing.T) {
	rig := newTestRig(t)
	rec := rig.callback("state=st-1&code=the-code", rig.start(t, "/"))
	if !cookieCleared(rec, loginCookie) || responseCookie(rec, loginCookie).Path != loginCookiePath {
		t.Errorf("login cookie not cleared: %+v", responseCookie(rec, loginCookie))
	}
}

// TestStartDropsOversizedReturnTo: an outsized return_to lands the reader on /
// instead of failing the sign-in.
func TestStartDropsOversizedReturnTo(t *testing.T) {
	rig := newTestRig(t)
	login := rig.start(t, "/t/"+strings.Repeat("w/d/long-path~", 400))
	rec := rig.callback("state=st-1&code=the-code", login)
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Errorf("Location = %q, want /", loc)
	}
}

// TestCallbackOnAnotherReplica: the login started on one replica finishes on
// another, and the session it mints passes either turnstile.
func TestCallbackOnAnotherReplica(t *testing.T) {
	first, second := newTestRig(t), newTestRig(t)
	login := first.start(t, "/d/index.md")
	rec := second.callback("state=st-1&code=the-code", login)
	if loc := rec.Header().Get("Location"); loc != "/d/index.md" {
		t.Fatalf("callback on second replica: Location = %q", loc)
	}
	cookie := responseCookie(rec, sessionCookie)
	if cookie == nil {
		t.Fatal("no session cookie")
	}
	if rec := first.probe(cookie); rec.Code != http.StatusOK || first.probeBearer != "id-tok" {
		t.Errorf("first replica probe: code %d, bearer %q", rec.Code, first.probeBearer)
	}
}

func TestCallbackDeniedAtIdP(t *testing.T) {
	rig := newTestRig(t)

	rec := rig.callback("state=st-1&error=access_denied", rig.start(t, ""))
	if loc := rec.Header().Get("Location"); loc != "/login?err=denied" {
		t.Errorf("Location = %q", loc)
	}
}

func TestLogout(t *testing.T) {
	rig := newTestRig(t)
	// Inside the refresh skew, so the next use of this cookie must refresh.
	cookie := rig.loginWith(t, session.Tokens{IDToken: "id-tok", RefreshToken: "ref-tok", Expiry: time.Now().Add(10 * time.Second)})

	req := httptest.NewRequest(http.MethodPost, "/logout", http.NoBody)
	req.AddCookie(cookie)
	rec := rig.do(req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("logout: code = %d", rec.Code)
	}
	if len(rig.auth.revoked) != 1 || rig.auth.revoked[0] != "ref-tok" {
		t.Errorf("revoked = %v, want [ref-tok]", rig.auth.revoked)
	}
	if !cookieCleared(rec, sessionCookie) {
		t.Error("logout left the session cookie")
	}

	// A replayed copy dies at its next refresh: the revoked grant mints
	// nothing. (One with a fresh id_token rides until that token expires.)
	if rec := rig.probe(cookie); rec.Code != http.StatusFound || !cookieCleared(rec, sessionCookie) {
		t.Errorf("replayed cookie after logout: code = %d, want 302 + cleared", rec.Code)
	}
}

// TestSessionBearerNeverSetsCookies: a public route lends a refreshed bearer
// but leaves the cookie alone, so no cacheable response carries a Set-Cookie.
func TestSessionBearerNeverSetsCookies(t *testing.T) {
	rig := newTestRig(t)
	var lent string
	rig.app.GET("/asset", func(c *echo.Context) error {
		lent = bearer.FromContext(c.Request().Context())
		return c.String(http.StatusOK, "css")
	}, SessionBearer(rig.sessions))
	cookie := rig.loginWith(t, session.Tokens{IDToken: "id-tok", RefreshToken: "ref-tok", Expiry: time.Now().Add(10 * time.Second)})

	req := httptest.NewRequest(http.MethodGet, "/asset", http.NoBody)
	req.AddCookie(cookie)
	rec := rig.do(req)
	if lent != "refreshed" {
		t.Errorf("bearer = %q, want the refreshed token", lent)
	}
	if got := rec.Header().Values("Set-Cookie"); len(got) != 0 {
		t.Errorf("public route set cookies: %v", got)
	}
}

// TestTurnstileRenewsRefreshedCookie: a refresh re-seals the session into a
// new cookie; the reader key survives it, and the new cookie needs no refresh.
func TestTurnstileRenewsRefreshedCookie(t *testing.T) {
	rig := newTestRig(t)
	cookie := rig.loginWith(t, session.Tokens{IDToken: "id-tok", RefreshToken: "ref-tok", Expiry: time.Now().Add(10 * time.Second)})

	rec := rig.probe(cookie)
	if rec.Code != http.StatusOK || rig.probeBearer != "refreshed" {
		t.Fatalf("probe: code %d, bearer %q", rec.Code, rig.probeBearer)
	}
	renewed := responseCookie(rec, sessionCookie)
	if renewed == nil || renewed.Value == cookie.Value || renewed.MaxAge <= 0 || renewed.MaxAge > int(time.Hour.Seconds()) {
		t.Fatalf("renewed cookie = %+v", renewed)
	}
	reader := rig.probeReader

	if rec := rig.probe(renewed); rec.Code != http.StatusOK || responseCookie(rec, sessionCookie) != nil {
		t.Errorf("renewed cookie probe: code %d, re-set %v", rec.Code, responseCookie(rec, sessionCookie) != nil)
	}
	if rig.probeReader != reader {
		t.Errorf("reader key changed across refresh: %q -> %q", reader, rig.probeReader)
	}
	if rig.auth.refreshes != 1 {
		t.Errorf("refreshes = %d, want 1", rig.auth.refreshes)
	}
}

func TestLoginPageRendersError(t *testing.T) {
	rig := newTestRig(t)

	rec := rig.do(httptest.NewRequest(http.MethodGet, "/login?err=denied", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), loginErrors["denied"]) {
		t.Error("login page missing the denied message")
	}

	// Unknown error codes render nothing — no reflected content channel.
	rec = rig.do(httptest.NewRequest(http.MethodGet, "/login?err=<script>alert(1)</script>", http.NoBody))
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
		{"/" + strings.Repeat("x", maxReturnTo), ""},
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
	staleCookie := rig.loginWith(t, session.Tokens{IDToken: "stale", RefreshToken: "r2", Expiry: time.Now().Add(-time.Minute)})

	rec := rig.probe(staleCookie)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("transient refresh failure: code = %d, want 502", rec.Code)
	}
	if responseCookie(rec, sessionCookie) != nil {
		t.Error("a transient failure must leave the session cookie alone")
	}

	// The healthy session is untouched.
	if rec := rig.probe(cookie); rec.Code != http.StatusOK {
		t.Errorf("healthy session: code = %d, want 200", rec.Code)
	}
}
