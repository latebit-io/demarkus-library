package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// authedApp wires the reading routes behind a middleware that marks every
// request as past the turnstile — the cataloging desk's affordances and write
// routes are auth-gated, so handler tests need an authenticated identity.
func authedApp(t *testing.T, svc *fakeReading) *echo.Echo {
	t.Helper()
	app := echo.New()
	view, err := NewView()
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}
	app.Renderer = view
	mark := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			c.Set(authedKey, true)
			return next(c)
		}
	}
	ReadingRoutes(app, NewReadingHandler(svc, "soul.demarkus.io", "/index.md"), mark)
	return app
}

func postForm(app *echo.Echo, target string, form url.Values) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	app.ServeHTTP(rec, req)
	return rec
}

func TestEditFormPrefills(t *testing.T) {
	svc := &fakeReading{draft: domain.EditDraft{
		Path: "/adr/0007.md", Body: "# ADR 7\n\nbody", Title: "ADR 7",
		Tags: []string{"adr", "decision"}, Importance: "0.9", Status: "accepted", Version: 7,
	}}
	rec := get(authedApp(t, svc), "/w/soul.demarkus.io/edit/adr/0007.md")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`action="/w/soul.demarkus.io/edit/adr/0007.md"`,
		`name="version" value="7"`,
		`name="title" value="ADR 7"`,
		`value="adr, decision"`,     // tags input
		`# ADR 7`,                   // body in the textarea
		`value="accepted" selected`, // status picker reflects current
		`hx-post="/w/soul.demarkus.io/preview"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("edit form missing %q", want)
		}
	}
	if svc.called != "EditDraft" {
		t.Errorf("called %q, want EditDraft", svc.called)
	}
}

func TestSaveEditPublishesAndRedirects(t *testing.T) {
	svc := &fakeReading{doc: domain.Document{Path: "/x.md"}}
	form := url.Values{
		"version":    {"3"},
		"title":      {"X"},
		"tags":       {"adr, decision"},
		"status":     {"accepted"},
		"importance": {"0.8"},
		"body":       {"# X\n\nnew body"},
	}
	rec := postForm(authedApp(t, svc), "/w/root/edit/x.md", form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/w/root/d/x.md" {
		t.Errorf("redirect = %q, want /w/root/d/x.md", loc)
	}
	if svc.gotBody != "# X\n\nnew body" || svc.gotVersion != 3 {
		t.Errorf("captured body/version = %q / %d", svc.gotBody, svc.gotVersion)
	}
	// Status folds into tags as the status: axis; metadata never in the body.
	if got := strings.Join(svc.gotMeta.Tags, ","); got != "adr,decision,status:accepted" {
		t.Errorf("tags = %q, want adr,decision,status:accepted", got)
	}
	if svc.gotMeta.Title != "X" || svc.gotMeta.Importance != "0.8" {
		t.Errorf("meta = %+v", svc.gotMeta)
	}
}

func TestSaveEditConflictReRendersWithBanner(t *testing.T) {
	svc := &fakeReading{publishErr: domain.ErrConflict}
	form := url.Values{"version": {"2"}, "body": {"# mine\n\nkeep this"}}
	rec := postForm(authedApp(t, svc), "/w/root/edit/x.md", form)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "changed since you opened it") {
		t.Errorf("conflict banner missing")
	}
	if !strings.Contains(body, "keep this") {
		t.Errorf("submitted body must be preserved on conflict")
	}
}

func TestSaveEditRejectsMalformedVersion(t *testing.T) {
	svc := &fakeReading{}
	form := url.Values{"version": {"not-a-number"}, "body": {"# x"}}
	rec := postForm(authedApp(t, svc), "/w/root/edit/x.md", form)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if svc.called == "Publish" {
		t.Errorf("must not publish with an unparseable version")
	}
}

func TestEditPreviewRendersFragment(t *testing.T) {
	svc := &fakeReading{}
	rec := postForm(authedApp(t, svc), "/w/root/preview", url.Values{"body": {"hello"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<p>hello</p>") {
		t.Errorf("preview fragment = %q", rec.Body.String())
	}
}

func TestEditAffordanceGatedOnAuth(t *testing.T) {
	svc := &fakeReading{doc: domain.Document{Title: "D", Path: "/adr/0007.md"}}
	// Unauthenticated: no edit link.
	if body := get(readingApp(t, svc), "/w/soul.demarkus.io/d/adr/0007.md").Body.String(); strings.Contains(body, "/edit/") {
		t.Errorf("edit affordance must be hidden without a session")
	}
	// Authenticated: edit link present.
	if body := get(authedApp(t, svc), "/w/soul.demarkus.io/d/adr/0007.md").Body.String(); !strings.Contains(body, `/w/soul.demarkus.io/edit/adr/0007.md`) {
		t.Errorf("edit affordance missing for an authed reader")
	}
}
