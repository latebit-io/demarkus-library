package web

import (
	"embed"
	"io/fs"

	"github.com/labstack/echo/v5"
)

// staticFS holds vendored front-end assets (htmx 2.0.10). We self-host rather
// than pull from a CDN: single Go binary, no external dependency at runtime,
// version pinned in the repo. See ADR 0003 (the htmx philosophy).
//
//go:embed static/*
var staticFS embed.FS

// StaticRoutes serves the embedded assets under /static/.
func StaticRoutes(e *echo.Echo) {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err) // embedded path is a compile-time constant; this cannot fail in practice
	}
	e.StaticFS("/static", sub)
}
