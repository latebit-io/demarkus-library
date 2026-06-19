package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
)

// csrfApp builds a minimal app behind CSRFMiddleware: a GET that renders the
// login form (exercising the {{ csrf }} renderer injection) and a protected
// POST that just 200s when the CSRF check passes.
func csrfApp(t *testing.T) *echo.Echo {
	t.Helper()
	app := echo.New()
	view, err := NewView()
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}
	app.Renderer = view
	app.Use(CSRFMiddleware(false))
	app.GET("/login", func(c *echo.Context) error {
		return c.Render(http.StatusOK, "login", loginPage{ReturnTo: "/"})
	})
	app.POST("/act", func(c *echo.Context) error { return c.NoContent(http.StatusOK) })
	return app
}

func TestCSRF_SecFetchSite(t *testing.T) {
	app := csrfApp(t)
	cases := []struct {
		name       string
		secFetch   string
		wantStatus int
	}{
		{"same-origin passes", "same-origin", http.StatusOK},
		{"direct-nav passes", "none", http.StatusOK},
		{"cross-site blocked", "cross-site", http.StatusForbidden},
		{"same-site blocked", "same-site", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/act", http.NoBody)
			req.Header.Set(echo.HeaderSecFetchSite, tc.secFetch)
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
		})
	}
}

// TestCSRF_NoTokenRejected: a legacy browser (no Sec-Fetch-Site) POSTing
// without a token is rejected — the core protection the audit flagged missing.
// A missing token field is a 400 (nothing to extract); a present-but-wrong
// token is a 403. Either way the write is blocked, never executed.
func TestCSRF_NoTokenRejected(t *testing.T) {
	app := csrfApp(t)
	req := httptest.NewRequest(http.MethodPost, "/act", http.NoBody)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 400 or 403 (rejected)", rec.Code)
	}
}

var csrfFieldRe = regexp.MustCompile(`name="_csrf" value="([^"]*)"`)

// TestCSRF_DoubleSubmit exercises the token fallback end-to-end: the GET renders
// the token into the form ({{ csrf }}) and sets the matching cookie; a POST that
// submits both passes, a mismatched token is rejected. No Sec-Fetch-Site header,
// so the token path (not the header shortcut) is what's under test.
func TestCSRF_DoubleSubmit(t *testing.T) {
	app := csrfApp(t)

	// GET the form: token lands in both the cookie and the rendered field.
	getRec := httptest.NewRecorder()
	app.ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, "/login", http.NoBody))
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET /login status = %d", getRec.Code)
	}
	var cookie *http.Cookie
	for _, c := range getRec.Result().Cookies() {
		if c.Name == csrfCookie {
			cookie = c
		}
	}
	if cookie == nil || cookie.Value == "" {
		t.Fatal("no csrf cookie set on GET")
	}
	m := csrfFieldRe.FindStringSubmatch(getRec.Body.String())
	if m == nil {
		t.Fatal("rendered form carried no _csrf field — {{ csrf }} not injected")
	}
	formToken := m[1]
	if formToken != cookie.Value {
		t.Fatalf("form token %q != cookie token %q (double-submit broken)", formToken, cookie.Value)
	}

	post := func(token string) int {
		form := url.Values{"_csrf": {token}}
		req := httptest.NewRequest(http.MethodPost, "/act", strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := post(formToken); code != http.StatusOK {
		t.Fatalf("matching token: status = %d, want 200", code)
	}
	if code := post("wrong-" + formToken); code != http.StatusForbidden {
		t.Fatalf("mismatched token: status = %d, want 403", code)
	}
}
