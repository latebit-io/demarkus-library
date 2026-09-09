package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// brandedApp is readingApp with an operator's branding layered on: a custom
// name plus logo/theme files served from a temp dir.
func brandedApp(t *testing.T, svc *fakeReading) *echo.Echo {
	t.Helper()
	dir := t.TempDir()
	logo := filepath.Join(dir, "logo.svg")
	css := filepath.Join(dir, "site.css")
	if err := os.WriteFile(logo, []byte("<svg xmlns='http://www.w3.org/2000/svg'/>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(css, []byte(":root { --paper: #001122; }"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := echo.New()
	view, err := NewView()
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}
	branding, err := ThemeRoutes(app, ThemeManifest{Name: "Acme Knowledge", Logo: logo, CSS: css})
	if err != nil {
		t.Fatalf("ThemeRoutes: %v", err)
	}
	app.Renderer = view.WithBranding(branding)
	ReadingRoutes(app, NewReadingHandler(svc, "soul.demarkus.io", "/index.md"))
	return app
}

func TestBrandingRendersNameLogoAndThemeLink(t *testing.T) {
	svc := &fakeReading{doc: domain.Document{Title: "X", Path: "/x.md", HTML: "<p>x</p>"}}
	rec := get(brandedApp(t, svc), "/t/soul.demarkus.io/d/x.md")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"<title>X — Acme Knowledge</title>",
		`<img src="` + ThemeLogoPath + `"`,
		`<link rel="stylesheet" href="` + ThemeCSSPath + `">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if strings.Contains(body, "demarkus Library") {
		t.Error("stock wordmark leaked into a branded page")
	}
}

func TestThemeRoutesServeAssets(t *testing.T) {
	svc := &fakeReading{doc: domain.Document{Title: "X", Path: "/x.md", HTML: "<p>x</p>"}}
	app := brandedApp(t, svc)

	rec := get(app, ThemeCSSPath)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "--paper: #001122") {
		t.Errorf("theme css: status %d body %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("theme css Content-Type = %q", ct)
	}

	rec = get(app, ThemeLogoPath)
	if rec.Code != http.StatusOK {
		t.Errorf("logo: status %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "svg") {
		t.Errorf("logo Content-Type = %q", ct)
	}
}

func TestThemeAssetsRevalidateViaETag(t *testing.T) {
	svc := &fakeReading{doc: domain.Document{Title: "X", Path: "/x.md", HTML: "<p>x</p>"}}
	app := brandedApp(t, svc)

	rec := get(app, ThemeCSSPath)
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("theme css missing ETag")
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want \"no-cache\"", cc)
	}

	req := httptest.NewRequest(http.MethodGet, ThemeCSSPath, http.NoBody)
	req.Header.Set("If-None-Match", etag)
	rec2 := httptest.NewRecorder()
	app.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusNotModified {
		t.Errorf("conditional GET: status %d, want 304", rec2.Code)
	}
	if rec2.Body.Len() != 0 {
		t.Errorf("304 carried a body (%d bytes)", rec2.Body.Len())
	}
}

func TestDefaultBrandingKeepsStockRoom(t *testing.T) {
	svc := &fakeReading{doc: domain.Document{Title: "X", Path: "/x.md", HTML: "<p>x</p>"}}
	rec := get(readingApp(t, svc), "/t/soul.demarkus.io/d/x.md")
	body := rec.Body.String()
	if !strings.Contains(body, "<title>X — demarkus Library</title>") {
		t.Error("default brand missing from title")
	}
	if strings.Contains(body, "<img") || strings.Contains(body, ThemeCSSPath) {
		t.Error("unbranded page carries logo or theme link")
	}
}

// manifestApp is readingApp under a manifest loaded from disk: a room-wide
// identity, a renamed universe, and one world (soul.demarkus.io) with its own
// name, logo, and stylesheet resolved relative to the manifest.
func manifestApp(t *testing.T, svc *fakeReading) *echo.Echo {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"branding.yaml": `
name: ACME Brain
css: site.css
terms:
  universe: Knowledge
worlds:
  soul.demarkus.io:
    name: Soul Room
    logo: soul.svg
    css: soul.css
`,
		"site.css": ":root { --paper: #001122; }",
		"soul.svg": "<svg xmlns='http://www.w3.org/2000/svg'/>",
		"soul.css": ":root { --paper: #332211; }",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m, err := LoadThemeManifest(filepath.Join(dir, "branding.yaml"))
	if err != nil {
		t.Fatalf("LoadThemeManifest: %v", err)
	}
	app := echo.New()
	view, err := NewView()
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}
	branding, err := ThemeRoutes(app, m)
	if err != nil {
		t.Fatalf("ThemeRoutes: %v", err)
	}
	app.Renderer = view.WithBranding(branding)
	WorldThemeRoutes(app, branding, nil)
	ReadingRoutes(app, NewReadingHandler(svc, "soul.demarkus.io", "/index.md").WithBranding(branding))
	return app
}

func TestManifestPerWorldBrandingOnFocusedWorld(t *testing.T) {
	svc := &fakeReading{doc: domain.Document{Title: "X", Path: "/x.md", HTML: "<p>x</p>"}}
	rec := get(manifestApp(t, svc), "/t/soul.demarkus.io/d/x.md")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"<title>X — Soul Room</title>",
		`<img src="/theme/worlds/soul.demarkus.io/logo"`,
		`<link rel="stylesheet" href="` + ThemeCSSPath + `">`,
		`<link rel="stylesheet" href="/theme/worlds/soul.demarkus.io/site.css">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	// The world stylesheet must ride in the body (htmx drops boosted heads)
	// and after the room theme so it wins the cascade.
	head := strings.Index(body, "</head>")
	world := strings.Index(body, "/theme/worlds/soul.demarkus.io/site.css")
	if head < 0 || world < head {
		t.Error("world stylesheet not in the body after the room theme")
	}
}

func TestManifestRoomBrandingOffWorld(t *testing.T) {
	svc := &fakeReading{doc: domain.Document{Title: "X", Path: "/x.md", HTML: "<p>x</p>"}}
	rec := get(manifestApp(t, svc), "/t/other.example/d/x.md")
	body := rec.Body.String()
	if !strings.Contains(body, "<title>X — ACME Brain</title>") {
		t.Error("room brand missing off the branded world")
	}
	if strings.Contains(body, "/theme/worlds/") {
		t.Error("world assets leaked onto another world's page")
	}
}

func TestManifestRenamesUniverse(t *testing.T) {
	svc := &fakeReading{doc: domain.Document{Title: "X", Path: "/x.md", HTML: "<p>x</p>"}}
	rec := get(manifestApp(t, svc), "/t/u")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"<title>Knowledge — ACME Brain</title>",
		`id="universe-title" class="graph-title">Knowledge<`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("floor missing %q", want)
		}
	}
	if strings.Contains(body, ">Universe<") || strings.Contains(body, ">universe<") {
		t.Error("stock universe term leaked into a renamed room")
	}
}

func TestWorldThemeAssetsServeAndMiss(t *testing.T) {
	svc := &fakeReading{doc: domain.Document{Title: "X", Path: "/x.md", HTML: "<p>x</p>"}}
	app := manifestApp(t, svc)
	rec := get(app, "/theme/worlds/soul.demarkus.io/site.css")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "#332211") {
		t.Errorf("world css: status %d body %q", rec.Code, rec.Body.String())
	}
	rec = get(app, "/theme/worlds/soul.demarkus.io/logo")
	if ct := rec.Header().Get("Content-Type"); rec.Code != http.StatusOK || !strings.Contains(ct, "svg") {
		t.Errorf("world logo: status %d type %q", rec.Code, ct)
	}
	if rec = get(app, "/theme/worlds/nope/logo"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown world asset: status %d, want 404", rec.Code)
	}
}

func TestLoadThemeManifestRejectsUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "branding.yaml")
	if err := os.WriteFile(path, []byte("name: x\nlogoo: y.svg\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadThemeManifest(path); err == nil {
		t.Error("typo'd key accepted")
	}
	if m, err := LoadThemeManifest(""); err != nil || m.Name != "" {
		t.Errorf("empty path: %+v, %v", m, err)
	}
}
