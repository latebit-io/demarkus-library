package web

import (
	"embed"
	"errors"
	"io/fs"
	"os"

	"github.com/labstack/echo/v5"
)

// staticFS holds vendored front-end assets (htmx 4.0.0 + its hx-sse extension),
// generated ones (chroma.css), and the room's own stylesheet (library.css —
// the design tokens and every register, shared by page.html and the login
// turnstile; the operator theme at /theme/site.css loads after it). We
// self-host rather than pull from a CDN:
// single Go binary, no external dependency at runtime, version pinned in the
// repo. See ADR 0003 (the htmx philosophy).
//
//go:generate go run gen_chroma_css.go
//go:embed static/*
var staticFS embed.FS

// StaticRoutes serves the embedded assets under /static/. overlayDir, when
// set (DEMARKUS_STATIC_DIR), shadows embedded files by name: an operator
// drops in a whole library.css to replace the stock sheet without a Go
// build, and anything not overridden still comes from the binary.
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
	if err == nil {
		return f, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return o.base.Open(name)
}
