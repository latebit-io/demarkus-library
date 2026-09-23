package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
)

func TestSecurityHeadersOnPagesAndAssets(t *testing.T) {
	svc := inWorldSvc("# Logo\n\nMark.\n\n```svg\n<svg xmlns='http://www.w3.org/2000/svg'/>\n```\n")
	app := echo.New()
	app.Use(SecurityHeaders())
	view, err := NewView()
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}
	brands := NewWorldBrands(svc)
	app.Renderer = view.WithWorldBrands(brands)
	WorldThemeRoutes(app, DefaultBranding(), brands)
	RoomRoutes(app, NewRoom(svc, "soul.demarkus.io", "/index.md"))

	rec := get(app, "/t/soul.demarkus.io/d/x.md")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{"script-src 'self';", "font-src 'self';", "connect-src 'self';", "object-src 'none';"} {
		if !strings.Contains(csp, want) {
			t.Errorf("page CSP missing %q: %q", want, csp)
		}
	}
	if strings.Contains(csp, "unsafe-eval") {
		t.Errorf("page CSP allows eval: %q", csp)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("nosniff = %q", got)
	}
	// A theme asset keeps its own, stricter policy.
	rec = get(app, "/theme/worlds/soul.demarkus.io/logo")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Security-Policy") != assetCSP {
		t.Errorf("asset: %d CSP %q", rec.Code, rec.Header().Get("Content-Security-Policy"))
	}
}

// The librarian's ask form must not rely on hx-on: the page CSP forbids the
// eval'd script htmx needs for it.
func TestTemplatesCarryNoInlineScript(t *testing.T) {
	entries, err := templatesFS.ReadDir("templates")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		body, err := templatesFS.ReadFile("templates/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"hx-on", "<script>", "onclick=", "javascript:"} {
			if strings.Contains(string(body), banned) {
				t.Errorf("%s contains %q, which the page CSP blocks", entry.Name(), banned)
			}
		}
	}
}
