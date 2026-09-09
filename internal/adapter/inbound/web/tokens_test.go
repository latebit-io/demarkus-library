package web

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

func TestTokensCSSRendersAndRejects(t *testing.T) {
	css, err := tokensCSS(map[string]string{"paper": "light-dark(#fff, #000)", "accent": "#0f766e"})
	if err != nil {
		t.Fatalf("valid tokens: %v", err)
	}
	for _, want := range []string{":root {", "--paper: light-dark(#fff, #000);", "--accent: #0f766e;"} {
		if !strings.Contains(css, want) {
			t.Errorf("rendered css missing %q: %q", want, css)
		}
	}
	if css, err := tokensCSS(nil); css != "" || err != nil {
		t.Errorf("no tokens: %q %v", css, err)
	}

	tests := []struct {
		name   string
		tokens map[string]string
	}{
		{name: "unknown token", tokens: map[string]string{"colour": "#fff"}},
		{name: "closes the rule", tokens: map[string]string{"paper": "#fff} body{display:none"}},
		{name: "ends the declaration", tokens: map[string]string{"ink": "#000; background: url(x)"}},
		{name: "fetches", tokens: map[string]string{"paper": "url(https://evil.example/x.png)"}},
		{name: "imports", tokens: map[string]string{"ink": "@import 'https://evil.example/x.css'"}},
		{name: "scripts", tokens: map[string]string{"accent": "expression(alert(1))"}},
		{name: "too long", tokens: map[string]string{"font-prose": strings.Repeat("a", tokenMaxLen+1)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tokensCSS(tt.tokens); err == nil {
				t.Error("accepted")
			}
		})
	}
}

// TestExampleManifestLoads keeps docs/branding-example honest: the shipped
// example is loaded and wired exactly as a deployment would.
func TestExampleManifestLoads(t *testing.T) {
	m, err := LoadThemeManifest(filepath.Join("..", "..", "..", "..", "docs", "branding-example", "branding.yaml"))
	if err != nil {
		t.Fatalf("LoadThemeManifest: %v", err)
	}
	app := echo.New()
	b, err := ThemeRoutes(app, m)
	if err != nil {
		t.Fatalf("ThemeRoutes: %v", err)
	}
	if b.Name != "ACME Brain" || b.Terms.Universe != "Knowledge" {
		t.Errorf("room identity = %q / %q", b.Name, b.Terms.Universe)
	}
	if b.FaviconURL != ThemeFaviconPath || b.LogoURL != ThemeLogoPath || b.TokensCSSURL != ThemeTokensPath {
		t.Errorf("asset urls = %+v", b)
	}
	WorldThemeRoutes(app, b, nil)
	if w := b.For("root"); w.Name != "ACME Handbook" || w.CSSURL == "" {
		t.Errorf("per-world entry = %+v", w)
	}
	rec := get(app, ThemeTokensPath)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "--paper:") {
		t.Errorf("tokens sheet: %d %q", rec.Code, rec.Body.String())
	}
	// A world declaring only tokens still gets a sheet, and it is real CSS.
	rec = get(app, "/theme/worlds/root/site.css")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "--accent: #b45309;") {
		t.Errorf("world tokens sheet: %d %q", rec.Code, rec.Body.String())
	}
}

func TestFaviconDefaultAndOverride(t *testing.T) {
	svc := &fakeReading{doc: domain.Document{Title: "X", Path: "/x.md", HTML: "<p>x</p>"}}
	rec := get(readingApp(t, svc), "/t/soul.demarkus.io/d/x.md")
	if !strings.Contains(rec.Body.String(), `<link rel="icon" href="`+DefaultFaviconURL+`">`) {
		t.Error("stock room did not link the built-in favicon")
	}

	app := brandedApp(t, svc)
	if rec = get(app, "/t/soul.demarkus.io/d/x.md"); !strings.Contains(rec.Body.String(), `href="`+ThemeFaviconPath+`"`) {
		t.Error("branded room did not link the operator favicon")
	}
	if rec = get(app, ThemeFaviconPath); rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "svg") {
		t.Errorf("favicon asset: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	// A bare /favicon.ico request is answered rather than 404ing.
	if rec = get(app, "/favicon.ico"); rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != ThemeFaviconPath {
		t.Errorf("/favicon.ico: %d -> %q", rec.Code, rec.Header().Get("Location"))
	}
}
