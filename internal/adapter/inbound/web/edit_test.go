package web

import (
	"errors"
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

func TestSaveEditMergeCandidateReRenders(t *testing.T) {
	// A stale edit returns a merge candidate, not an error: the editor re-opens
	// with the merged body, the version to resolve at, and a notice — the work
	// is advanced and preserved, not lost to a bare reload.
	svc := &fakeReading{publishCand: &domain.MergeCandidate{
		Body:             "intro\n<<<<<<< yours\nmine\n=======\ntheirs\n>>>>>>> current\n",
		PublishAtVersion: 9,
		HasMarkers:       true,
	}}
	form := url.Values{"version": {"7"}, "body": {"mine"}}
	rec := postForm(authedApp(t, svc), "/w/root/edit/x.md", form)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"some lines conflict",      // the merge notice (HasMarkers branch)
		`name="version" value="9"`, // re-publish at the broker's resolve version
		"theirs",                   // the merged body, dropped into the textarea
	} {
		if !strings.Contains(body, want) {
			t.Errorf("merge re-render missing %q", want)
		}
	}
	// The conflict banner (error) must NOT show — a merge is not an error.
	if strings.Contains(body, "changed since you opened it") {
		t.Errorf("merge candidate should not render the conflict error banner")
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

func TestSaveEditNotFoundMaps404(t *testing.T) {
	svc := &fakeReading{publishErr: domain.ErrNotFound}
	form := url.Values{"version": {"2"}, "body": {"# gone"}}
	rec := postForm(authedApp(t, svc), "/w/root/edit/x.md", form)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (ErrNotFound)", rec.Code)
	}
}

func TestSaveEditUnknownErrorMaps502(t *testing.T) {
	svc := &fakeReading{publishErr: errors.New("broker exploded")}
	form := url.Values{"version": {"2"}, "body": {"# keep me"}}
	rec := postForm(authedApp(t, svc), "/w/root/edit/x.md", form)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 (unmapped error)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "keep me") {
		t.Errorf("submitted body must be preserved on an unexpected error")
	}
}

func TestNewFormCreateMode(t *testing.T) {
	rec := get(authedApp(t, &fakeReading{}), "/w/root/new?dir=/plans/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`action="/w/root/new"`,        // create posts to /new, no path in the action
		`name="path" value="/plans/"`, // path field pre-filled from ?dir
		`>create</button>`,            // create-mode button label
	} {
		if !strings.Contains(body, want) {
			t.Errorf("new form missing %q", want)
		}
	}
	// Create mode carries no version field (version 0 is implicit).
	if strings.Contains(body, `name="version"`) {
		t.Errorf("create form must not render a version field")
	}
}

func TestCreateDocPublishesAtVersionZero(t *testing.T) {
	svc := &fakeReading{doc: domain.Document{Path: "/plans/new.md"}}
	form := url.Values{
		"path":   {"/plans/new.md"},
		"title":  {"New Plan"},
		"tags":   {"plans"},
		"status": {"draft"},
		"body":   {"# New Plan"},
	}
	rec := postForm(authedApp(t, svc), "/w/root/new", form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/w/root/d/plans/new.md" {
		t.Errorf("redirect = %q", loc)
	}
	if svc.gotVersion != 0 {
		t.Errorf("expected_version = %d, want 0 (create sentinel)", svc.gotVersion)
	}
	if got := strings.Join(svc.gotMeta.Tags, ","); got != "plans,status:draft" {
		t.Errorf("tags = %q", got)
	}
}

func TestCreateDocRejectsDirectoryPath(t *testing.T) {
	svc := &fakeReading{}
	rec := postForm(authedApp(t, svc), "/w/root/new", url.Values{"path": {"/plans/"}, "body": {"x"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if svc.called == "Publish" {
		t.Errorf("a directory path must not publish")
	}
	if !strings.Contains(rec.Body.String(), "not a directory") {
		t.Errorf("expected a path-guidance banner")
	}
}

func TestCreateDocPathExistsConflict(t *testing.T) {
	svc := &fakeReading{publishErr: domain.ErrConflict}
	rec := postForm(authedApp(t, svc), "/w/root/new", url.Values{"path": {"/plans/x.md"}, "body": {"x"}})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "already exists") {
		t.Errorf("create conflict must say the path is taken, got: %s", rec.Body.String())
	}
}

func TestNewAffordanceGatedAndFolderScoped(t *testing.T) {
	svc := &fakeReading{doc: domain.Document{Title: "D", Path: "/plans/x.md"}}
	// Unauthenticated: no new link.
	if body := get(readingApp(t, svc), "/w/root/d/plans/x.md").Body.String(); strings.Contains(body, "/new?dir=") {
		t.Errorf("new affordance must be hidden without a session")
	}
	// Authenticated: new link pre-fills the current folder.
	body := get(authedApp(t, svc), "/w/root/d/plans/x.md").Body.String()
	if !strings.Contains(body, "/w/root/new?dir=%2Fplans%2F") {
		t.Errorf("new affordance missing or wrong folder: %s", body)
	}
}

func TestAppendFormBodyOnly(t *testing.T) {
	rec := get(authedApp(t, &fakeReading{}), "/w/root/append/log.md")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `action="/w/root/append/log.md"`) {
		t.Errorf("append form action wrong")
	}
	// Append carries no metadata fields and no version (match input markup, not
	// the CSS selectors in <head> that mention name="title"/name="tags").
	for _, absent := range []string{`name="title" value=`, `name="tags" value=`, `name="path"`, `name="version"`} {
		if strings.Contains(body, absent) {
			t.Errorf("append form must not render %q", absent)
		}
	}
	if !strings.Contains(body, "edit-append-note") {
		t.Errorf("append form should explain it only adds content")
	}
}

func TestAppendDocAddsAndRedirects(t *testing.T) {
	svc := &fakeReading{doc: domain.Document{Path: "/log.md"}}
	rec := postForm(authedApp(t, svc), "/w/root/append/log.md", url.Values{"body": {"\n- new entry"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if rec.Header().Get("Location") != "/w/root/d/log.md" {
		t.Errorf("redirect = %q", rec.Header().Get("Location"))
	}
	if svc.called != "Append" || svc.gotBody != "\n- new entry" {
		t.Errorf("append not invoked with body: called=%q body=%q", svc.called, svc.gotBody)
	}
}

func TestAppendDocRejectsEmpty(t *testing.T) {
	svc := &fakeReading{}
	rec := postForm(authedApp(t, svc), "/w/root/append/log.md", url.Values{"body": {"   \n  "}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if svc.called == "Append" {
		t.Errorf("empty append must not write")
	}
	if !strings.Contains(rec.Body.String(), "Nothing to append") {
		t.Errorf("expected empty-append guidance")
	}
}

func TestAppendAffordanceGatedOnAuth(t *testing.T) {
	svc := &fakeReading{doc: domain.Document{Title: "D", Path: "/log.md"}}
	if body := get(readingApp(t, svc), "/w/root/d/log.md").Body.String(); strings.Contains(body, "/append/") {
		t.Errorf("append affordance must be hidden without a session")
	}
	if body := get(authedApp(t, svc), "/w/root/d/log.md").Body.String(); !strings.Contains(body, "/w/root/append/log.md") {
		t.Errorf("append affordance missing for an authed reader")
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

func TestNormalizeNewPath(t *testing.T) {
	ok := map[string]string{
		"/notes/idea.md": "/notes/idea.md",
		"notes/idea.md":  "/notes/idea.md", // leading slash added
		"  /x.md  ":      "/x.md",          // trimmed
	}
	for in, want := range ok {
		if got, valid := normalizeNewPath(in); !valid || got != want {
			t.Errorf("normalizeNewPath(%q) = %q,%v want %q,true", in, got, valid, want)
		}
	}
	for _, bad := range []string{"", "   ", "/", "/plans/", "/a//b.md"} {
		if _, valid := normalizeNewPath(bad); valid {
			t.Errorf("normalizeNewPath(%q) accepted, want rejected", bad)
		}
	}
}

func TestDirOf(t *testing.T) {
	cases := map[string]string{"/index.md": "/", "/plans/x.md": "/plans/", "/a/b/c.md": "/a/b/"}
	for in, want := range cases {
		if got := dirOf(in); got != want {
			t.Errorf("dirOf(%q) = %q, want %q", in, got, want)
		}
	}
}
