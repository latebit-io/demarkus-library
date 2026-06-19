// Command demarkus-library is the web front-end ("Universe Library") for a
// demarkus universe: a server-rendered reading room over a knowledge system.
//
// This is the composition root of a hexagonal (ports & adapters) architecture:
// it constructs the outbound adapters (a world gateway, a goldmark renderer),
// injects them into the application core, and exposes the core through the
// inbound web adapter (Echo).
//
// Two transports, selected by DEMARKUS_TRANSPORT (config.go):
//
//   - quic: the Phase 0/1a demo path — one world read directly over the
//     demarkus QUIC fetch client, no login.
//   - broker: the Phase 1b library card (ADR 0004) — reads go through the
//     broker's MCP gateway with the reader's bearer; the reading room sits
//     behind the org-login turnstile (OAuth code + PKCE as a registered
//     confidential web client, tokens server-side, opaque session cookie).
//
// The core and the reading-room web handlers are identical in both modes —
// only the adapters wired here change.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"github.com/latebit-io/demarkus-library/internal/adapter/inbound/web"
	"github.com/latebit-io/demarkus-library/internal/adapter/inbound/web/session"
	"github.com/latebit-io/demarkus-library/internal/adapter/outbound/broker"
	"github.com/latebit-io/demarkus-library/internal/adapter/outbound/cache"
	"github.com/latebit-io/demarkus-library/internal/adapter/outbound/federated"
	"github.com/latebit-io/demarkus-library/internal/adapter/outbound/markdown"
	"github.com/latebit-io/demarkus-library/internal/adapter/outbound/oauth"
	"github.com/latebit-io/demarkus-library/internal/adapter/outbound/world"
	"github.com/latebit-io/demarkus-library/internal/core/port"
	"github.com/latebit-io/demarkus-library/internal/core/service"
	"github.com/latebit-io/demarkus/client/fetch"
)

// sweepInterval is how often expired sessions and abandoned logins are
// collected in broker mode. Lazy expiry keeps correctness regardless; the
// sweep just bounds memory.
const sweepInterval = time.Hour

const (
	// maxBodyBytes caps request bodies — generous for markdown source (the
	// cataloging-desk writes) but bounded against memory-exhaustion POSTs.
	maxBodyBytes = 2 << 20 // 2 MiB

	// handlerTimeout bounds per-request handler time so a slow or wedged
	// world read returns 503 rather than pinning a goroutine indefinitely.
	handlerTimeout = 30 * time.Second

	// readHeaderTimeout and idleTimeout harden the transport against slow or
	// idle clients (slowloris, leaked keep-alives). Echo defaults ReadTimeout
	// to 30s; WriteTimeout is deliberately left unset so a future SSE librarian
	// (Phase 4) isn't capped — handlerTimeout bounds normal request latency.
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 120 * time.Second
)

// version is stamped by goreleaser via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	versionFlag := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *versionFlag {
		fmt.Println("demarkus-library", version)
		os.Exit(0)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	config, err := NewAppConfig()
	if err != nil {
		logger.Error("configuration invalid", "err", err)
		os.Exit(1)
	}

	// Inbound adapter (driving): Echo.
	app := echo.New()
	app.Use(middleware.Recover())
	// Cap request bodies before any handler reads them (write POSTs).
	app.Use(middleware.BodyLimit(maxBodyBytes))
	// CSRF on every state-changing request (the cataloging-desk writes and the
	// login/logout forms). Cookie posture follows the session cookie's Secure
	// flag. Forms emit the token via {{ csrf }} (view.go).
	app.Use(web.CSRFMiddleware(config.CookieSecure))
	// Bound handler latency: a wedged outbound read returns 503 instead of
	// pinning the goroutine. Covers the turnstile's token refresh too.
	app.Use(middleware.ContextTimeout(handlerTimeout))

	view, err := web.NewView()
	if err != nil {
		panic(err)
	}
	app.Renderer = view

	web.StaticRoutes(app)
	web.HealthRoutes(app, web.NewHealthHandler())

	// Outbound adapters (driven) — the transport decides the world gateway
	// and whether the reading room sits behind the turnstile. Both modes
	// wrap their transports in the federated router so mark:// links across
	// the distributed graph resolve (host-shaped worlds dial direct,
	// knowledge-system names go through the broker).
	renderer := markdown.NewRenderer()
	var gateway port.WorldGateway
	var defaultWorld string
	var turnstile []echo.MiddlewareFunc
	var shutdown func()

	switch config.Transport {
	case TransportQUIC:
		client := fetch.NewClient(fetch.Options{Insecure: config.Insecure})
		defaultWorld = world.NormalizeHost(config.Host)
		gateway = federated.New(federated.Config{
			Hosts:         world.NewGateway(client, config.Host, config.ReadToken),
			HomeHost:      config.Host,
			AllowExternal: config.Federation,
		})
		shutdown = client.Close

	case TransportBroker:
		auth := brokerAuth{client: oauth.NewClient(oauth.Config{
			BrokerURL:    config.BrokerURL,
			ClientID:     config.ClientID,
			ClientSecret: config.ClientSecret,
			RedirectURI:  config.RedirectURI,
			Scopes:       config.Scopes,
		}, nil)}

		store := session.NewMemoryStore(config.SessionTTL)
		pending := session.NewPendingStore()
		sessions := session.NewManager(store, auth, 0)

		web.AuthRoutes(app, web.NewAuthHandler(auth, sessions, pending, web.CookieConfig{
			Secure: config.CookieSecure,
			TTL:    config.SessionTTL,
		}))
		turnstile = append(turnstile, web.RequireSession(sessions))

		bg := broker.NewGateway(config.BrokerURL, nil)
		fcfg := federated.Config{Names: bg, AllowExternal: config.Federation}
		var fclient *fetch.Client
		if config.Federation {
			// Federation reads are tokenless and anonymous: external
			// hosts get no home credential and no bearer.
			fclient = fetch.NewClient(fetch.Options{Insecure: config.Insecure})
			fcfg.Hosts = world.NewGateway(fclient, "", "")
		}
		defaultWorld = config.World
		gateway = federated.New(fcfg)
		stopSweeper := startSweeper(store, pending)
		shutdown = func() {
			stopSweeper()
			bg.Close()
			if fclient != nil {
				fclient.Close()
			}
		}
	}

	// Application core (the hexagon) + the reading room, same in both modes.
	// The rendered-document cache backs the trail engine's 1-read-per-click
	// budget (ADR 0005 decision 9).
	reading := service.NewReadingService(gateway, renderer, cache.NewMemory(0)).WithHub(config.Hub)
	web.ReadingRoutes(app, web.NewReadingHandler(reading, defaultWorld, config.DefaultDoc), turnstile...)

	logger.Info("demarkus Library reading room starting",
		"port", config.Port, "transport", config.Transport,
		"world", worldLabel(config), "default_doc", config.DefaultDoc)

	// Echo v5's Start/StartTLS handle SIGINT/SIGTERM: they drain in-flight
	// requests with a 10s graceful timeout and then return. Transport
	// cleanup runs afterwards (not via defer, which os.Exit would skip).
	err = serve(app, config)
	shutdown()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

// serve starts the HTTP server, over TLS when a cert/key pair is configured
// (local dev against the broker's https-only redirect rule; in the cluster
// the ingress terminates TLS instead). Cert and key are read here and passed
// as contents — StartTLS's path form reads through fs.FS rooted at ".",
// which rejects absolute paths.
func serve(app *echo.Echo, config *AppConfig) error {
	// Own the signal context for both paths (Echo.Start wires its own, but the
	// TLS path needs an explicit one and both share the hardened StartConfig).
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	sc := echo.StartConfig{
		Address: fmt.Sprintf(":%d", config.Port),
		BeforeServeFunc: func(s *http.Server) error {
			s.ReadHeaderTimeout = readHeaderTimeout
			s.IdleTimeout = idleTimeout
			return nil
		},
	}
	if config.TLSCert == "" {
		return sc.Start(ctx, app)
	}
	cert, err := os.ReadFile(config.TLSCert)
	if err != nil {
		return fmt.Errorf("read DEMARKUS_TLS_CERT: %w", err)
	}
	key, err := os.ReadFile(config.TLSKey)
	if err != nil {
		return fmt.Errorf("read DEMARKUS_TLS_KEY: %w", err)
	}
	return sc.StartTLS(ctx, app, cert, key)
}

// startSweeper collects expired sessions and abandoned pending logins on a
// ticker; the returned stop function halts it on shutdown.
func startSweeper(store *session.MemoryStore, pending *session.PendingStore) func() {
	ticker := time.NewTicker(sweepInterval)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ticker.C:
				store.Sweep()
				pending.Sweep()
			case <-done:
				return
			}
		}
	}()
	return func() {
		ticker.Stop()
		close(done)
	}
}

// worldLabel names the world being served for the startup log line.
func worldLabel(config *AppConfig) string {
	if config.Transport == TransportBroker {
		return config.World + " via " + config.BrokerURL
	}
	return config.Host
}
