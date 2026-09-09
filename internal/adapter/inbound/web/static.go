package web

import (
	"embed"
	"errors"
	"io/fs"
	"os"

	"github.com/labstack/echo/v5"
)

// staticFS holds the vendored front-end assets (htmx and hx-sse), the
// generated chroma.css, and the room's own library.css. Self-hosted, never a
// CDN: one binary, no runtime dependency, versions pinned (ADR 0003).
//
//go:generate go run gen_chroma_css.go
//go:embed static/*
var staticFS embed.FS

// StaticRoutes serves the embedded assets under /static/. overlayDir
// (DEMARKUS_STATIC_DIR) shadows them by name, so an operator can replace
// library.css outright without a Go build.
func StaticRoutes(e *echo.Echo, overlayDir string) {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err) // embedded path is a compile-time constant; this cannot fail in practice
	}
	if overlayDir != "" {
		sub = overlayFS{top: os.DirFS(overlayDir), base: sub}
	}
	e.StaticFS("/static", sub)
}

// overlayFS opens from top first and falls back to base when top has no such
// file. Only Open is layered: directory listings are not served, so a merged
// ReadDir is unnecessary.
type overlayFS struct {
	top, base fs.FS
}

func (o overlayFS) Open(name string) (fs.File, error) {
	f, err := o.top.Open(name)
	switch {
	case err == nil:
		return f, nil
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, fs.ErrInvalid):
		// Absent, or a name os.DirFS refuses but the embedded FS accepts:
		// either way the overlay has nothing to say, so serve the binary's.
		return o.base.Open(name)
	}
	return nil, err
}
