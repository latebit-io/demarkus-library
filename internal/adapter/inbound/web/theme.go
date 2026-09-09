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
	themeWorldsPrefix = "/theme/worlds/"
	themeCSSType      = "text/css; charset=utf-8"
)

// ThemeManifest is the operator's branding declaration (DEMARKUS_BRANDING, a
// YAML file): the room-wide identity, the display vocabulary, and per-world
// overrides. Asset paths resolve relative to the manifest's directory unless
// absolute, so a Helm ConfigMap holding the manifest and its assets side by
// side needs no path plumbing. Unknown keys are errors: a typo must stop
// startup, not silently leave the stock room.
type ThemeManifest struct {
	Name   string                `yaml:"name"`
	Logo   string                `yaml:"logo"`
	CSS    string                `yaml:"css"`
	Terms  Terms                 `yaml:"terms"`
	Worlds map[string]WorldTheme `yaml:"worlds"`

	// Dir is the directory relative asset paths resolve against (the
	// manifest's own); empty ⇒ paths are used as given.
	Dir string `yaml:"-"`
}

// WorldTheme is one world's branding override: any field left empty inherits
// the room-wide value (the world stylesheet loads after the room theme, so it
// overrides rather than replaces).
type WorldTheme struct {
	Name string `yaml:"name"`
	Logo string `yaml:"logo"`
	CSS  string `yaml:"css"`
}

// Terms is the room's display vocabulary. Universe names the whole-knowledge
// scope (the floor, the overlay, the dock anchor); an operator whose readers
// say "Knowledge" or "Brain" renames it here. Route segments and internal
// scope keys never change — only what readers see.
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

// ThemeRoutes loads the manifest's branding assets and registers their
// routes, returning the Branding the view should render under. Assets are
// read once at startup — they are deploy-time files like the embedded
// static/ bundle, and in-memory serving sidesteps echo's cwd-rooted fs.FS
// (absolute paths are the common case). An unset path registers nothing and
// leaves the matching URL empty, so templates omit the affordance entirely.
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
		for _, a := range []struct {
			file, path, ctype string
			url               *string
		}{
			{"logo", m.resolve(wt.Logo), "", &wb.LogoURL},
			{"site.css", m.resolve(wt.CSS), themeCSSType, &wb.CSSURL},
		} {
			if a.path == "" {
				continue
			}
			blob, err := os.ReadFile(a.path)
			if err != nil {
				return b, fmt.Errorf("worlds.%s: %w", world, err)
			}
			assets[world+"/"+a.file] = blobHandler(assetType(a.ctype, a.path, blob), blob)
			*a.url = themeWorldsPrefix + world + "/" + a.file
		}
		b.Worlds[world] = wb
	}
	b.assets = assets
	return b, nil
}

// WorldThemeRoutes serves per-world assets: one param route (world names are
// hostnames or system names; a lookup by the decoded :world param stays
// correct whatever the router does with escaping). In-world documents win
// over the manifest's files, matching the view's resolution order. brands
// may be nil (no reading service, as in tests of the file path alone).
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

// assetCSP keeps a theme asset inert if a reader navigates to it directly:
// no scripts, no fetches, and a sandboxed origin — so a world's SVG can
// never run as a page with the library's origin and session. Loaded as an
// <img> or a stylesheet, browsers ignore the header, so nothing rendered
// changes.
const assetCSP = "default-src 'none'; style-src 'unsafe-inline'; sandbox"

// blobHandler serves a loaded asset under a revalidation caching model: an
// ETag over the content with `no-cache` (cache, but ask first), so repeat
// navigations cost a 304 instead of the body — and a rebrand shows on the
// reader's next request, which a long max-age on the stable /theme/* URLs
// would defer. The type is declared, never sniffed.
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
