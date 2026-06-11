// Command demarkus-library is the web front-end ("Universe Library") for a
// demarkus universe: a server-rendered reading room over a knowledge system.
//
// This is the composition root of a hexagonal (ports & adapters) architecture:
// it constructs the outbound adapters (a demarkus world gateway over QUIC, a
// goldmark renderer), injects them into the application core, and exposes the
// core through the inbound web adapter (Echo).
//
// Phase 0 (foundation spike): FETCH a demarkus document and render it
// server-side, served as HTML. It reads a world directly over QUIC, staying
// auth-free against a dev/soul world — the broker MCP gateway adapter and org
// OAuth land in Phase 1. Plan: mark://soul.demarkus.io/plans/universe-library.md.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"github.com/latebit/demarkus-library/internal/adapter/inbound/web"
	"github.com/latebit/demarkus-library/internal/adapter/outbound/markdown"
	"github.com/latebit/demarkus-library/internal/adapter/outbound/world"
	"github.com/latebit/demarkus-library/internal/core/service"
	"github.com/latebit/demarkus/client/fetch"
)

func main() {
	versionFlag := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *versionFlag {
		fmt.Println("demarkus-library (phase 0)")
		os.Exit(0)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	config, err := NewAppConfig()
	if err != nil {
		panic(err)
	}

	// Outbound adapters (driven). Phase 1 swaps the world gateway for an MCP
	// client over the broker; the core and inbound adapter are unaffected.
	client := fetch.NewClient(fetch.Options{Insecure: config.Insecure})
	defer client.Close()
	gateway := world.NewGateway(client, config.Host, config.ReadToken)
	renderer := markdown.NewRenderer()

	// Application core (the hexagon).
	reading := service.NewReadingService(gateway, renderer)

	// Inbound adapter (driving): Echo.
	app := echo.New()
	app.Use(middleware.Recover())

	view, err := web.NewView()
	if err != nil {
		panic(err)
	}
	app.Renderer = view

	web.StaticRoutes(app)
	web.ReadingRoutes(app, web.NewReadingHandler(reading, config.DefaultDoc))
	web.HealthRoutes(app, web.NewHealthHandler())

	logger.Info("demarkus Library reading room starting",
		"port", config.Port, "world", config.Host, "default_doc", config.DefaultDoc)

	if err := app.Start(fmt.Sprintf(":%d", config.Port)); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error(err.Error())
		os.Exit(1)
	}
}
