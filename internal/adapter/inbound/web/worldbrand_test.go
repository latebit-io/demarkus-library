package web

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// inWorldSvc is a reading fake whose world publishes its own branding under
// /.well-known/library/.
func inWorldSvc(logoBody string) *fakeReading {
	return &fakeReading{
		doc: domain.Document{Title: "X", Path: "/x.md", HTML: "<p>x</p>"},
		raws: map[string]domain.RawDocument{
			WorldBrandDoc:  {Body: "# Branding\n\nThis world's identity.\n\n```yaml\nname: Soul Room\n```\n"},
			WorldBrandCSS:  {Body: "# Stylesheet\n\nTokens.\n\n```css\n:root { --paper: #abcdef; }\n```\n"},
			WorldBrandLogo: {Body: logoBody},
		},
	}
}

func inWorldApp(t *testing.T, svc *fakeReading) (*echo.Echo, *WorldBrands) {
	t.Helper()
	app := echo.New()
	view, err := NewView()
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}
	branding, err := ThemeRoutes(app, ThemeManifest{Name: "ACME Brain"})
	if err != nil {
		t.Fatalf("ThemeRoutes: %v", err)
	}
	brands := NewWorldBrands(svc)
	app.Renderer = view.WithBranding(branding).WithWorldBrands(brands)
	WorldThemeRoutes(app, branding, brands)
	ReadingRoutes(app, NewReadingHandler(svc, "soul.demarkus.io", "/index.md").WithBranding(branding))
	return app, brands
}

func TestInWorldBrandingRendersAndServes(t *testing.T) {
	svc := inWorldSvc("# Logo\n\nThe mark.\n\n```svg\n<svg xmlns='http://www.w3.org/2000/svg'/>\n```\n")
	app, _ := inWorldApp(t, svc)
	rec := get(app, "/t/soul.demarkus.io/d/x.md")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"<title>X — Soul Room</title>",
		`<img src="/theme/worlds/soul.demarkus.io/logo"`,
		`<link rel="stylesheet" href="/theme/worlds/soul.demarkus.io/site.css">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	rec = get(app, "/theme/worlds/soul.demarkus.io/site.css")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "#abcdef") || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/css") {
		t.Errorf("css: %d %q %q", rec.Code, rec.Body.String(), rec.Header().Get("Content-Type"))
	}
	rec = get(app, "/theme/worlds/soul.demarkus.io/logo")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "svg") || strings.Contains(rec.Body.String(), "```") {
		t.Errorf("logo: %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
}

func TestInWorldLogoBase64(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}
	body := wrapFence("Logo", "The mark.", "base64", "image/png", base64.StdEncoding.EncodeToString(png))
	app, _ := inWorldApp(t, inWorldSvc(body))
	rec := get(app, "/theme/worlds/soul.demarkus.io/logo")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" || rec.Body.Len() != len(png) {
		t.Errorf("png logo: %d %q %d bytes", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
	}
}

func TestInWorldBrandingAbsentFallsBack(t *testing.T) {
	svc := &fakeReading{doc: domain.Document{Title: "X", Path: "/x.md", HTML: "<p>x</p>"}, raws: map[string]domain.RawDocument{}}
	app, brands := inWorldApp(t, svc)
	rec := get(app, "/t/soul.demarkus.io/d/x.md")
	if !strings.Contains(rec.Body.String(), "<title>X — ACME Brain</title>") {
		t.Error("room brand missing when the world declares none")
	}
	if rec = get(app, "/theme/worlds/soul.demarkus.io/logo"); rec.Code != http.StatusNotFound {
		t.Errorf("absent logo: status %d", rec.Code)
	}
	// Absence is cached: the anchor doc is read once per TTL, not per render.
	svc.calls = nil
	get(app, "/t/soul.demarkus.io/d/x.md")
	if _, ok := brands.For(context.Background(), "soul.demarkus.io"); ok {
		t.Error("unbranded world reported a brand")
	}
}

func TestWorldBrandsCacheTTLAndInvalidate(t *testing.T) {
	svc := inWorldSvc("")
	brands := NewWorldBrands(svc)
	now := time.Unix(0, 0)
	brands.now = func() time.Time { return now }
	reads := func() int {
		n := 0
		for _, c := range svc.calls {
			if strings.HasPrefix(c, "Raw") {
				n++
			}
		}
		return n
	}
	svc.trackRaw = true
	brands.For(context.Background(), "w")
	brands.For(context.Background(), "w")
	if got := reads(); got != 3 {
		t.Errorf("reads after two resolves = %d, want 3 (one load)", got)
	}
	now = now.Add(worldBrandTTL + time.Second)
	brands.For(context.Background(), "w")
	if got := reads(); got != 6 {
		t.Errorf("reads after TTL = %d, want 6", got)
	}
	brands.Invalidate("w")
	brands.For(context.Background(), "w")
	if got := reads(); got != 9 {
		t.Errorf("reads after invalidate = %d, want 9", got)
	}
}

func TestFirstFenceAndWrap(t *testing.T) {
	body := wrapFence("Logo", "Summary.", "base64", "image/png", "AAAA\nBBBB")
	lang, info, content, ok := firstFence(body)
	if !ok || lang != "base64" || info != "image/png" || content != "AAAA\nBBBB" {
		t.Errorf("round trip: %q %q %q %v", lang, info, content, ok)
	}
	if _, _, _, ok := firstFence("# No fence\n\nplain text"); ok {
		t.Error("matched a fence in plain text")
	}
	if a := decodeLogo("```svg\n" + strings.Repeat("x", worldBrandMaxBytes+1) + "\n```"); a != nil {
		t.Error("oversized logo accepted")
	}
}
