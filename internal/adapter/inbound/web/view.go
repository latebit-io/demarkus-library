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

// Branding is the operator's identity layer over the room's chrome: the
// display name replaces the "demarkus Library" wordmark in titles, nav, and
// the login card; LogoURL and ThemeCSSURL point at the /theme/ assets
// (ThemeRoutes), empty ⇒ the affordance is simply absent. Templates read it
// via the brand/logoURL/themeCSS funcs so no view model carries it.
type Branding struct {
	Name        string
	LogoURL     string
	ThemeCSSURL string
}

// DefaultBranding is the stock room: the demarkus wordmark, no logo, no
// override stylesheet.
func DefaultBranding() Branding { return Branding{Name: "demarkus Library"} }

// View implements echo.Renderer over the embedded templates.
type View struct {
	templates *template.Template
	branding  Branding
}

// NewView parses the embedded templates. Returns an error so wiring can fail
// loudly at startup rather than on first request.
func NewView() (*View, error) {
	// csrf is a per-request function the renderer overrides on a clone; a
	// no-op placeholder must exist at parse time so templates referencing
	// {{ csrf }} compile (likewise the branding funcs, bound per-render from
	// v.branding). The base template is only ever cloned, never executed,
	// which keeps the per-request Funcs override on the clone valid.
	t, err := template.New("library").
		Funcs(template.FuncMap{
			"csrf":     func() string { return "" },
			"brand":    func() string { return "" },
			"logoURL":  func() string { return "" },
			"themeCSS": func() string { return "" },
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
	v.branding = b
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
	cl.Funcs(template.FuncMap{
		"csrf":     func() string { return token },
		"brand":    func() string { return b.Name },
		"logoURL":  func() string { return b.LogoURL },
		"themeCSS": func() string { return b.ThemeCSSURL },
	})
	return cl.ExecuteTemplate(w, name, data)
}
