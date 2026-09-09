package web

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// deskApp is authedApp with in-world branding wired: the desk reads the
// current documents through WorldBrands and writes through the fake.
func deskApp(t *testing.T, svc *fakeReading) (*echo.Echo, *WorldBrands) {
	t.Helper()
	app := echo.New()
	view, err := NewView()
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}
	brands := NewWorldBrands(svc)
	app.Renderer = view.WithWorldBrands(brands)
	WorldThemeRoutes(app, DefaultBranding(), brands)
	mark := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			c.Set(authedKey, true)
			return next(c)
		}
	}
	ReadingRoutes(app, NewReadingHandler(svc, "soul.demarkus.io", "/index.md").WithWorldBrands(brands), mark)
	return app, brands
}

func TestBrandingDeskPrefills(t *testing.T) {
	svc := inWorldSvc("# Logo\n\nMark.\n\n```svg\n<svg xmlns='http://www.w3.org/2000/svg'/>\n```\n")
	app, _ := deskApp(t, svc)
	rec := get(app, "/w/soul.demarkus.io/branding")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`action="/w/soul.demarkus.io/branding"`,
		`name="name" value="Soul Room"`,
		`--paper: #abcdef`,
		`<img src="/theme/worlds/soul.demarkus.io/logo" alt="current logo">`,
		`name="clear_logo"`,
		`enctype="multipart/form-data"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("desk missing %q", want)
		}
	}
}

func TestBrandingDeskSavePublishesFencedDocs(t *testing.T) {
	svc := &fakeReading{raws: map[string]domain.RawDocument{}, editErr: domain.ErrNotFound,
		doc: domain.Document{Title: "X", Path: "/x.md", HTML: "<p>x</p>"}}
	app, brands := deskApp(t, svc)
	brands.For(t.Context(), "soul.demarkus.io") // warm the (empty) cache so the save must invalidate it
	rec := postForm(app, "/w/soul.demarkus.io/branding", url.Values{
		"name":     {"ACME: Brain"},
		"css":      {":root { --paper: #fff; }"},
		"logo_svg": {"<svg xmlns='http://www.w3.org/2000/svg'/>"},
	})
	if rec.Code != http.StatusSeeOther || !strings.HasSuffix(rec.Header().Get("Location"), "/branding?saved=1") {
		t.Fatalf("save: status %d location %q body %q", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	for path, want := range map[string]string{
		WorldBrandDoc:  "```yaml\nname: 'ACME: Brain'\n```",
		WorldBrandCSS:  "```css\n:root { --paper: #fff; }\n```",
		WorldBrandLogo: "```svg\n<svg xmlns='http://www.w3.org/2000/svg'/>\n```",
	} {
		got := svc.gotBodies[path]
		if !strings.HasPrefix(got, "# ") || !strings.Contains(got, want) {
			t.Errorf("%s body = %q, want H1 + %q", path, got, want)
		}
	}
	if svc.gotVersion != 0 {
		t.Errorf("new document published at version %d, want 0", svc.gotVersion)
	}
	if len(svc.gotMeta.Tags) == 0 {
		t.Error("branding documents published untagged")
	}
	// The resolver re-reads after the save (the cache was invalidated).
	if b, ok := brands.For(t.Context(), "soul.demarkus.io"); !ok || b.Name != "ACME: Brain" {
		t.Errorf("after save: %+v %v", b, ok)
	}
}

func TestBrandingDeskSaveLeavesUntouchedAndClears(t *testing.T) {
	svc := inWorldSvc("")
	svc.draft = domain.EditDraft{Version: 4}
	app, _ := deskApp(t, svc)
	rec := postForm(app, "/w/soul.demarkus.io/branding", url.Values{"name": {"Soul"}, "clear_css": {"on"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
	}
	if _, ok := svc.gotBodies[WorldBrandLogo]; ok {
		t.Error("logo republished though the form left it alone")
	}
	if css := svc.gotBodies[WorldBrandCSS]; strings.Contains(css, "```") {
		t.Errorf("cleared stylesheet still carries a fence: %q", css)
	}
	if svc.gotVersion != 4 {
		t.Errorf("existing document published at version %d, want 4", svc.gotVersion)
	}
}

func TestBrandingDeskUploadsBinaryLogo(t *testing.T) {
	svc := &fakeReading{raws: map[string]domain.RawDocument{}, editErr: domain.ErrNotFound}
	app, _ := deskApp(t, svc)
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("name", "Soul")
	fw, _ := w.CreateFormFile("logo_file", "logo.png")
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}
	_, _ = fw.Write(png)
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, "/w/soul.demarkus.io/branding", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
	}
	got := svc.gotBodies[WorldBrandLogo]
	if !strings.Contains(got, "```base64 image/png\n") {
		t.Errorf("png logo body = %q", got)
	}
	if a := decodeLogo(got); a == nil || a.ctype != "image/png" || !bytes.Equal(a.blob, png) {
		t.Errorf("round trip failed: %+v", a)
	}
}

func TestBrandingDeskRejectsBadLogo(t *testing.T) {
	svc := &fakeReading{raws: map[string]domain.RawDocument{}, editErr: domain.ErrNotFound}
	app, _ := deskApp(t, svc)
	rec := postForm(app, "/w/soul.demarkus.io/branding", url.Values{"logo_svg": {"not svg"}})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "SVG markup") {
		t.Errorf("bad svg: status %d", rec.Code)
	}
	if svc.gotBodies != nil {
		t.Error("published despite a rejected logo")
	}
}

func TestBrandingDeskPartialSaveIsReported(t *testing.T) {
	svc := &fakeReading{raws: map[string]domain.RawDocument{}, editErr: domain.ErrNotFound,
		publishErrFor: map[string]error{WorldBrandCSS: domain.ErrWriteUnsupported}}
	app, brands := deskApp(t, svc)
	brands.For(t.Context(), "soul.demarkus.io")
	rec := postForm(app, "/w/soul.demarkus.io/branding", url.Values{"name": {"Soul"}, "css": {":root{}"}})
	if rec.Code == http.StatusSeeOther {
		t.Fatal("partial save redirected as success")
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Saved branding.") || !strings.Contains(body, "Stylesheet was not saved") {
		t.Errorf("partial save not reported: %q", body[strings.Index(body, "edit-error"):min(len(body), strings.Index(body, "edit-error")+200)])
	}
	if _, ok := svc.gotBodies[WorldBrandDoc]; !ok {
		t.Error("anchor document was not written before the failure")
	}
	// The failed field keeps what was typed, so a retry costs no retyping.
	if !strings.Contains(body, ":root{}") {
		t.Error("submitted stylesheet was dropped from the redisplayed form")
	}
	// The cache was dropped after the successful write, so the desk shows
	// what is stored now rather than the submitted stylesheet.
	if b, ok := brands.For(t.Context(), "soul.demarkus.io"); !ok || b.Name != "Soul" {
		t.Errorf("persisted name after partial save: %+v %v", b, ok)
	}
}

func TestBrandingDeskRejectsScriptedSVGUpload(t *testing.T) {
	svc := &fakeReading{raws: map[string]domain.RawDocument{}, editErr: domain.ErrNotFound}
	app, _ := deskApp(t, svc)
	rec := postForm(app, "/w/soul.demarkus.io/branding", url.Values{
		"logo_svg": {"<svg xmlns='http://www.w3.org/2000/svg'><script>alert(1)</script></svg>"},
	})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "scripts") {
		t.Errorf("scripted svg: status %d", rec.Code)
	}
}
