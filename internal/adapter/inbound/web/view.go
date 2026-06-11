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

// View implements echo.Renderer over the embedded templates.
type View struct {
	templates *template.Template
}

// NewView parses the embedded templates. Returns an error so wiring can fail
// loudly at startup rather than on first request.
func NewView() (*View, error) {
	t, err := template.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &View{templates: t}, nil
}

// Render satisfies echo.Renderer (v5 signature: context first, then writer).
func (v *View) Render(_ *echo.Context, w io.Writer, name string, data any) error {
	return v.templates.ExecuteTemplate(w, name, data)
}
