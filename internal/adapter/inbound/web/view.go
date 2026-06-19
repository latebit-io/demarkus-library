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

// View implements echo.Renderer over the embedded templates.
type View struct {
	templates *template.Template
}

// NewView parses the embedded templates. Returns an error so wiring can fail
// loudly at startup rather than on first request.
func NewView() (*View, error) {
	// csrf is a per-request function the renderer overrides on a clone; a
	// no-op placeholder must exist at parse time so templates referencing
	// {{ csrf }} compile. The base template is only ever cloned, never
	// executed, which keeps the per-request Funcs override on the clone valid.
	t, err := template.New("library").
		Funcs(template.FuncMap{"csrf": func() string { return "" }}).
		ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &View{templates: t}, nil
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
	cl.Funcs(template.FuncMap{"csrf": func() string { return token }})
	return cl.ExecuteTemplate(w, name, data)
}
