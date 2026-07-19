package web

import (
	"mime"
	"net/http"
	"os"
	"path/filepath"

	"github.com/labstack/echo/v5"
)

// Theme asset URLs: the operator-supplied branding files (DEMARKUS_LOGO /
// DEMARKUS_THEME_CSS) served under stable paths the templates reference via
// the logoURL/themeCSS funcs (view.go).
const (
	ThemeLogoPath = "/theme/logo"
	ThemeCSSPath  = "/theme/site.css"
)

// ThemeRoutes loads the configured branding assets and registers their
// routes, returning the Branding the view should render under. Assets are
// read once at startup — they are deploy-time files like the embedded
// static/ bundle, and in-memory serving sidesteps echo's cwd-rooted fs.FS
// (absolute paths are the common case). An unset path registers nothing and
// leaves the matching URL empty, so templates omit the affordance entirely.
func ThemeRoutes(e *echo.Echo, brandName, logoPath, cssPath string) (Branding, error) {
	b := DefaultBranding()
	if brandName != "" {
		b.Name = brandName
	}
	if logoPath != "" {
		blob, err := os.ReadFile(logoPath)
		if err != nil {
			return b, err
		}
		// The logo may be any image format; the URL carries no extension,
		// so the content type comes from the source path (or a sniff).
		ctype := mime.TypeByExtension(filepath.Ext(logoPath))
		if ctype == "" {
			ctype = http.DetectContentType(blob)
		}
		e.GET(ThemeLogoPath, func(c *echo.Context) error {
			return c.Blob(http.StatusOK, ctype, blob)
		})
		b.LogoURL = ThemeLogoPath
	}
	if cssPath != "" {
		blob, err := os.ReadFile(cssPath)
		if err != nil {
			return b, err
		}
		e.GET(ThemeCSSPath, func(c *echo.Context) error {
			return c.Blob(http.StatusOK, "text/css; charset=utf-8", blob)
		})
		b.ThemeCSSURL = ThemeCSSPath
	}
	return b, nil
}
