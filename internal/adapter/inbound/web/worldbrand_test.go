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

// fencedLogo wraps bytes the way a published logo.md carries them.
func fencedLogo(lang, info, content string) string {
	return fencedDoc{Title: "Logo", Summary: "The mark.", Fence: fence{Lang: lang, Info: info, Content: content}}.markdown()
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
	RoomRoutes(app, NewRoom(svc, "soul.demarkus.io", "/index.md").WithBranding(branding))
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
	body := fencedLogo("base64", "image/png", base64.StdEncoding.EncodeToString(png))
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
	svc.trackRaw = true
	svc.calls = nil
	get(app, "/t/soul.demarkus.io/d/x.md")
	for _, call := range svc.calls {
		if strings.HasPrefix(call, "Raw ") {
			t.Errorf("cached absence re-read the anchor: %v", svc.calls)
		}
	}
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
	body := fencedLogo("base64", "image/png", "AAAA\nBBBB")
	got, ok := firstFence(body)
	want := fence{Lang: "base64", Info: "image/png", Content: "AAAA\nBBBB"}
	if !ok || got != want {
		t.Errorf("round trip: %+v ok=%v, want %+v", got, ok, want)
	}
	if _, ok := firstFence("# No fence\n\nplain text"); ok {
		t.Error("matched a fence in plain text")
	}
	if asset := decodeLogo("```svg\n" + strings.Repeat("x", worldBrandMaxBytes+1) + "\n```"); asset != nil {
		t.Error("oversized logo accepted")
	}
}

func TestInWorldLogoRejectsActiveOrMislabeledContent(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}
	tests := []struct {
		name string
		body string
	}{
		{name: "html as base64", body: fencedLogo("base64", "text/html", base64.StdEncoding.EncodeToString([]byte("<html><script>alert(1)</script>")))},
		{name: "svg with script", body: fencedLogo("svg", "", "<svg xmlns='http://www.w3.org/2000/svg'><script>alert(1)</script></svg>")},
		{name: "svg with event handler", body: fencedLogo("svg", "", "<svg xmlns='http://www.w3.org/2000/svg' onload='alert(1)'/>")},
		{name: "svg with external reference", body: fencedLogo("svg", "", "<svg xmlns='http://www.w3.org/2000/svg'><image href='https://evil.example/x.png'/></svg>")},
		{name: "svg declared as png", body: fencedLogo("base64", "image/png", base64.StdEncoding.EncodeToString([]byte("<svg xmlns='http://www.w3.org/2000/svg'/>")))},
		{name: "png declared as svg", body: fencedLogo("base64", "image/svg+xml", base64.StdEncoding.EncodeToString(png))},
		{name: "unsupported type", body: fencedLogo("base64", "application/pdf", base64.StdEncoding.EncodeToString([]byte("%PDF-1.4")))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if asset := decodeLogo(tt.body); asset != nil {
				t.Errorf("accepted as %s", asset.ctype)
			}
		})
	}
	if asset := decodeLogo(fencedLogo("base64", "", base64.StdEncoding.EncodeToString(png))); asset == nil || asset.ctype != "image/png" {
		t.Errorf("undeclared png: %+v", asset)
	}
}

func TestWorldAssetsServeHardened(t *testing.T) {
	svc := inWorldSvc("# Logo\n\nMark.\n\n```svg\n<svg xmlns='http://www.w3.org/2000/svg'/>\n```\n")
	app, _ := inWorldApp(t, svc)
	for _, path := range []string{"/theme/worlds/soul.demarkus.io/logo", "/theme/worlds/soul.demarkus.io/site.css"} {
		rec := get(app, path)
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: missing nosniff", path)
		}
		if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") || !strings.Contains(csp, "default-src 'none'") {
			t.Errorf("%s: CSP = %q", path, csp)
		}
	}
}

func TestFenceSurvivesBackticksInContent(t *testing.T) {
	css := "/* a ``` in a comment */\n:root { --x: 1; }\n````\nstill css"
	body := fencedDoc{Title: "Stylesheet", Summary: "s", Fence: fence{Lang: "css", Content: css}}.markdown()
	got, ok := firstFence(body)
	if !ok || got.Lang != "css" || got.Content != css {
		t.Errorf("round trip lost content: ok=%v %+v", ok, got)
	}
}

func TestBrandingNameSurvivesYAMLFolding(t *testing.T) {
	long := strings.Repeat("A very long brand name ", 8) + "end"
	body := fencedDoc{Title: "Branding", Summary: "s", Fence: fence{Lang: "yaml", Content: brandingYAML(brandingForm{name: long})}}.markdown()
	svc := &fakeReading{raws: map[string]domain.RawDocument{WorldBrandDoc: {Body: body}}}
	b, ok := NewWorldBrands(svc).For(context.Background(), "w")
	if !ok || b.Name != long {
		t.Errorf("name = %q, ok=%v", b.Name, ok)
	}
}

func TestInWorldTokensPrecedeTheStylesheet(t *testing.T) {
	// The fixture's stylesheet sets --paper: #abcdef; the tokens set a
	// different value, so the served order is visible.
	svc := inWorldSvc("")
	svc.raws[WorldBrandDoc] = domain.RawDocument{Body: fencedDoc{
		Title: "Branding", Summary: "s",
		Fence: fence{Lang: "yaml", Content: "name: Soul Room\ntheme:\n  paper: \"#123456\"\n"},
	}.markdown()}
	app, brands := inWorldApp(t, svc)
	rec := get(app, "/theme/worlds/soul.demarkus.io/site.css")
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("world sheet: %d %q", rec.Code, body)
	}
	tokens, sheet := strings.Index(body, "#123456"), strings.Index(body, "#abcdef")
	if tokens < 0 || sheet < 0 || sheet < tokens {
		t.Errorf("stylesheet did not follow the tokens: %q", body)
	}
	desk := brands.Desk(t.Context(), "soul.demarkus.io")
	if desk.Tokens["paper"] != "#123456" || strings.Contains(desk.CSS, "#123456") {
		t.Errorf("desk mixed tokens into the stylesheet field: %+v", desk)
	}
}

func TestInWorldUnsafeTokensAreDropped(t *testing.T) {
	svc := inWorldSvc("")
	svc.raws[WorldBrandDoc] = domain.RawDocument{Body: fencedDoc{
		Title: "Branding", Summary: "s",
		Fence: fence{Lang: "yaml", Content: "name: Soul Room\ntheme:\n  paper: \"#fff} body{display:none\"\n"},
	}.markdown()}
	app, _ := inWorldApp(t, svc)
	body := get(app, "/theme/worlds/soul.demarkus.io/site.css").Body.String()
	if strings.Contains(body, "display:none") {
		t.Errorf("unsafe token reached the served sheet: %q", body)
	}
	if strings.TrimSpace(body) != ":root { --paper: #abcdef; }" {
		t.Errorf("dropping the tokens changed the stylesheet: %q", body)
	}
}
