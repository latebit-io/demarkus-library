// Package web is the inbound (driving) adapter: an Echo HTTP surface that drives
// the reading room through the inbound port. It depends on port.ReadingService,
// never on the concrete service or any outbound adapter.
package web

import (
	"embed"
	"html/template"
	"io"

	"github.com/labstack/echo/v5"
)

//go:embed templates/*.html
var templatesFS embed.FS

// csrfContextKey is where CSRFMiddleware stashes the per-request token. The
// renderer reads it to expose {{ csrf }} to templates so every form can carry
// the token (see csrf.go).
const csrfContextKey = "csrf"

// Branding is the operator's identity layer over the room's chrome: name,
// assets, per-world overrides (resolved by For), and the display vocabulary.
// Templates read it through funcs keyed by the view model's World.
type Branding struct {
	Name        string
	LogoURL     string
	ThemeCSSURL string
	Terms       Terms
	Worlds      map[string]WorldBranding

	assets map[string]echo.HandlerFunc // manifest world assets, keyed <world>/<file> (WorldThemeRoutes)
}

// WorldBranding is one world's resolved override; empty fields inherit.
type WorldBranding struct {
	Name    string
	LogoURL string
	CSSURL  string
}

// DefaultBranding is the stock room: the demarkus wordmark, no logo, no
// override stylesheet, stock vocabulary.
func DefaultBranding() Branding { return Branding{Name: "demarkus Library", Terms: DefaultTerms()} }

// For resolves what the chrome shows while a world is in focus: the world's
// own name and logo when declared, else the room's; CSSURL is the world's
// stylesheet (empty ⇒ none), loaded after the room theme so it overrides.
func (b Branding) For(world string) WorldBranding {
	r := WorldBranding{Name: b.Name, LogoURL: b.LogoURL}
	w, ok := b.Worlds[world]
	if !ok {
		return r
	}
	if w.Name != "" {
		r.Name = w.Name
	}
	if w.LogoURL != "" {
		r.LogoURL = w.LogoURL
	}
	r.CSSURL = w.CSSURL
	return r
}

// View implements echo.Renderer over the embedded templates.
type View struct {
	templates *template.Template
	branding  Branding
	worlds    *WorldBrands // in-world branding (ADR 0008); nil ⇒ file manifest only
}

// NewView parses the embedded templates. Returns an error so wiring can fail
// loudly at startup rather than on first request.
func NewView() (*View, error) {
	// Placeholders so templates calling these funcs compile; Render binds the
	// real ones on a clone. The base template is only ever cloned, never
	// executed, which keeps that per-request override valid.
	t, err := template.New("library").
		Funcs(template.FuncMap{
			"csrf":     func() string { return "" },
			"brand":    func(string) string { return "" },
			"logoURL":  func(string) string { return "" },
			"worldCSS": func(string) string { return "" },
			"themeCSS": func() string { return "" },
			"universe": func() string { return "" },
		}).
		ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &View{templates: t, branding: DefaultBranding()}, nil
}

// WithBranding returns the view rendering under the given branding.
func (v *View) WithBranding(b Branding) *View {
	if b.Name == "" {
		b.Name = DefaultBranding().Name
	}
	if b.Terms.Universe == "" {
		b.Terms = DefaultTerms()
	}
	v.branding = b
	return v
}

// WithWorldBrands lets worlds brand themselves through their own documents,
// resolved ahead of the manifest's per-world entries.
func (v *View) WithWorldBrands(w *WorldBrands) *View {
	v.worlds = w
	return v
}

// Render satisfies echo.Renderer (v5 signature: context first, then writer). It
// binds the request's CSRF token to {{ csrf }} on a per-request clone so forms
// can emit it without every view model carrying a token field.
func (v *View) Render(c *echo.Context, w io.Writer, name string, data any) error {
	cl, err := v.templates.Clone()
	if err != nil {
		return err
	}
	token, _ := c.Get(csrfContextKey).(string)
	b := v.branding
	// One resolution per world per render: the title, nav, logo, and
	// stylesheet funcs all ask for the same world, and the in-world lookup
	// is a (cached) read.
	memo := map[string]WorldBranding{}
	resolve := func(world string) WorldBranding {
		if r, ok := memo[world]; ok {
			return r
		}
		r := b.For(world)
		if world != "" && v.worlds != nil {
			if iw, ok := v.worlds.For(c.Request().Context(), world); ok {
				if iw.Name != "" {
					r.Name = iw.Name
				}
				if iw.LogoURL != "" {
					r.LogoURL = iw.LogoURL
				}
				if iw.CSSURL != "" {
					r.CSSURL = iw.CSSURL
				}
			}
		}
		memo[world] = r
		return r
	}
	cl.Funcs(template.FuncMap{
		"csrf":     func() string { return token },
		"brand":    func(world string) string { return resolve(world).Name },
		"logoURL":  func(world string) string { return resolve(world).LogoURL },
		"worldCSS": func(world string) string { return resolve(world).CSSURL },
		"themeCSS": func() string { return b.ThemeCSSURL },
		"universe": func() string { return b.Terms.Universe },
	})
	return cl.ExecuteTemplate(w, name, data)
}
