package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/labstack/echo/v5"
	"gopkg.in/yaml.v3"
)

// Theme asset URLs: the operator-supplied branding files served under stable
// paths the templates reference via the logoURL/themeCSS/worldCSS funcs
// (view.go). Per-world assets sit under themeWorldsPrefix/<world>/<file>.
const (
	ThemeLogoPath     = "/theme/logo"
	ThemeCSSPath      = "/theme/site.css"
	ThemeTokensPath   = "/theme/tokens.css"
	ThemeFaviconPath  = "/theme/favicon"
	DefaultFaviconURL = "/static/favicon.svg"
	themeWorldsPrefix = "/theme/worlds/"
	themeCSSType      = "text/css; charset=utf-8"
)

// ThemeManifest is the operator's branding declaration (DEMARKUS_BRANDING).
// Relative asset paths resolve against the manifest's own directory, so a
// ConfigMap carries both. Unknown keys stop startup rather than pass silently.
type ThemeManifest struct {
	Name    string                `yaml:"name"`
	Logo    string                `yaml:"logo"`
	Favicon string                `yaml:"favicon"`
	CSS     string                `yaml:"css"`
	Theme   map[string]string     `yaml:"theme"`
	Terms   Terms                 `yaml:"terms"`
	Worlds  map[string]WorldTheme `yaml:"worlds"`

	// Dir is the directory relative asset paths resolve against (the
	// manifest's own); empty ⇒ paths are used as given.
	Dir string `yaml:"-"`
}

// WorldTheme is one world's branding override: any field left empty inherits
// the room-wide value (the world stylesheet loads after the room theme, so it
// overrides rather than replaces).
type WorldTheme struct {
	Name  string            `yaml:"name"`
	Logo  string            `yaml:"logo"`
	CSS   string            `yaml:"css"`
	Theme map[string]string `yaml:"theme"`
}

// Terms is the room's display vocabulary. Universe names the whole-knowledge
// scope readers see (floor, overlay, dock); routes and internal scope keys
// keep their own names.
type Terms struct {
	Universe string `yaml:"universe"`
}

// DefaultTerms is the stock vocabulary.
func DefaultTerms() Terms { return Terms{Universe: "Universe"} }

// UniverseLower is the term in running text ("the knowledge floor").
func (t Terms) UniverseLower() string { return strings.ToLower(t.Universe) }

// LoadThemeManifest reads a manifest file; an empty path is the empty
// manifest (the stock room), so callers need not branch.
func LoadThemeManifest(path string) (ThemeManifest, error) {
	var m ThemeManifest
	if path == "" {
		return m, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil && !errors.Is(err, io.EOF) {
		return m, fmt.Errorf("%s: %w", path, err)
	}
	m.Dir = filepath.Dir(path)
	return m, nil
}

// resolve turns a manifest-relative asset path into one the loader can open.
func (m ThemeManifest) resolve(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || filepath.IsAbs(p) || m.Dir == "" {
		return p
	}
	return filepath.Join(m.Dir, p)
}

// ThemeRoutes loads the manifest's assets and registers their routes,
// returning the Branding to render under. Assets are read once at startup;
// an unset path registers nothing, so templates omit that affordance.
func ThemeRoutes(e *echo.Echo, m ThemeManifest) (Branding, error) {
	b := DefaultBranding()
	if n := strings.TrimSpace(m.Name); n != "" {
		b.Name = n
	}
	if u := strings.TrimSpace(m.Terms.Universe); u != "" {
		b.Terms.Universe = u
	}
	var err error
	if b.LogoURL, err = serveAsset(e, ThemeLogoPath, m.resolve(m.Logo), ""); err != nil {
		return b, fmt.Errorf("logo: %w", err)
	}
	if b.FaviconURL, err = serveAsset(e, ThemeFaviconPath, m.resolve(m.Favicon), ""); err != nil {
		return b, fmt.Errorf("favicon: %w", err)
	}
	if b.FaviconURL == "" {
		b.FaviconURL = DefaultFaviconURL
	}
	// Bare /favicon.ico is requested without a link, so answer it too.
	e.GET("/favicon.ico", func(c *echo.Context) error {
		return c.Redirect(http.StatusMovedPermanently, b.FaviconURL)
	})
	tokens, err := tokensCSS(m.Theme)
	if err != nil {
		return b, fmt.Errorf("theme: %w", err)
	}
	if tokens != "" {
		e.GET(ThemeTokensPath, blobHandler(themeCSSType, []byte(tokens)))
		b.TokensCSSURL = ThemeTokensPath
	}
	if b.ThemeCSSURL, err = serveAsset(e, ThemeCSSPath, m.resolve(m.CSS), themeCSSType); err != nil {
		return b, fmt.Errorf("css: %w", err)
	}
	if len(m.Worlds) == 0 {
		return b, nil
	}
	assets := map[string]echo.HandlerFunc{}
	b.Worlds = make(map[string]WorldBranding, len(m.Worlds))
	for world, wt := range m.Worlds {
		world = strings.TrimSpace(world)
		if world == "" {
			return b, errors.New("worlds: empty world name")
		}
		wb := WorldBranding{Name: strings.TrimSpace(wt.Name)}
		if path := m.resolve(wt.Logo); path != "" {
			blob, err := os.ReadFile(path)
			if err != nil {
				return b, fmt.Errorf("worlds.%s.logo: %w", world, err)
			}
			assets[world+"/logo"] = blobHandler(assetType("", path, blob), blob)
			wb.LogoURL = themeWorldsPrefix + world + "/logo"
		}
		// A world's tokens and its stylesheet share one sheet: htmx keeps only
		// the title of a boosted head, so the world theme is one body link.
		sheet, err := tokensCSS(wt.Theme)
		if err != nil {
			return b, fmt.Errorf("worlds.%s.theme: %w", world, err)
		}
		if path := m.resolve(wt.CSS); path != "" {
			blob, err := os.ReadFile(path)
			if err != nil {
				return b, fmt.Errorf("worlds.%s.css: %w", world, err)
			}
			sheet += string(blob)
		}
		if sheet != "" {
			assets[world+"/site.css"] = blobHandler(themeCSSType, []byte(sheet))
			wb.CSSURL = themeWorldsPrefix + world + "/site.css"
		}
		b.Worlds[world] = wb
	}
	b.assets = assets
	return b, nil
}

// WorldThemeRoutes serves per-world assets from one param route, so a world
// name with dots or escapes still matches. In-world documents win over the
// manifest's files, as they do in the view; brands may be nil.
func WorldThemeRoutes(e *echo.Echo, b Branding, brands *WorldBrands) {
	e.GET(themeWorldsPrefix+":world/:file", func(c *echo.Context) error {
		world, file := c.Param("world"), c.Param("file")
		if a, ok := brands.Asset(c.Request().Context(), world, file); ok {
			return blobHandler(a.ctype, a.blob)(c)
		}
		if h, ok := b.assets[world+"/"+file]; ok {
			return h(c)
		}
		return echo.NewHTTPError(http.StatusNotFound, "no such theme asset")
	})
}

// serveAsset registers a startup-loaded asset at route and returns the route,
// or "" when path is empty (nothing registered).
func serveAsset(e *echo.Echo, route, path, ctype string) (string, error) {
	if path == "" {
		return "", nil
	}
	blob, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	e.GET(route, blobHandler(assetType(ctype, path, blob), blob))
	return route, nil
}

// assetType is the content type for an asset: as given, else by the source
// path's extension (the served URL carries none), else sniffed.
func assetType(ctype, path string, blob []byte) string {
	if ctype != "" {
		return ctype
	}
	if t := mime.TypeByExtension(filepath.Ext(path)); t != "" {
		return t
	}
	return http.DetectContentType(blob)
}

// assetCSP keeps a theme asset inert when a reader navigates straight to it,
// so a world's SVG cannot run as a page on the library's origin. Browsers
// ignore it when the asset loads as an <img> or a stylesheet.
const assetCSP = "default-src 'none'; style-src 'unsafe-inline'; sandbox"

// blobHandler serves a loaded asset with an ETag and `no-cache`: repeat
// navigations cost a 304, and a rebrand still shows on the next request,
// which a long max-age on these stable URLs would defer.
func blobHandler(ctype string, blob []byte) func(*echo.Context) error {
	sum := sha256.Sum256(blob)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	return func(c *echo.Context) error {
		h := c.Response().Header()
		h.Set("ETag", etag)
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", assetCSP)
		if strings.Contains(c.Request().Header.Get("If-None-Match"), etag) {
			return c.NoContent(http.StatusNotModified)
		}
		return c.Blob(http.StatusOK, ctype, blob)
	}
}
