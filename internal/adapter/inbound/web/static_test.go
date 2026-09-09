package web

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
)

func TestStaticOverlayShadowsEmbedded(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "library.css"), []byte("/* mine */ :root{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := echo.New()
	StaticRoutes(app, dir)
	rec := get(app, "/static/library.css")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "/* mine */") {
		t.Errorf("overlay css: %d %q", rec.Code, rec.Body.String()[:min(40, rec.Body.Len())])
	}
	// Not overridden ⇒ still embedded.
	if rec = get(app, "/static/htmx.min.js"); rec.Code != http.StatusOK || rec.Body.Len() < 1000 {
		t.Errorf("embedded fallback: %d %d bytes", rec.Code, rec.Body.Len())
	}
	// No overlay ⇒ stock sheet.
	plain := echo.New()
	StaticRoutes(plain, "")
	if rec = get(plain, "/static/library.css"); strings.Contains(rec.Body.String(), "/* mine */") {
		t.Error("stock route served the overlay")
	}
}
