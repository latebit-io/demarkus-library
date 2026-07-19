package web

import (
	"net/http"
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
	branding, err := ThemeRoutes(app, "Acme Knowledge", logo, css)
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
