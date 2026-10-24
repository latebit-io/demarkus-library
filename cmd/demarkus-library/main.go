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
//     confidential web client, tokens sealed in an HttpOnly session cookie).
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
	"path/filepath"
	"syscall"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"github.com/latebit-io/demarkus-library/internal/adapter/inbound/web"
	"github.com/latebit-io/demarkus-library/internal/adapter/inbound/web/session"
	"github.com/latebit-io/demarkus-library/internal/adapter/librarian"
	"github.com/latebit-io/demarkus-library/internal/adapter/outbound/broker"
	"github.com/latebit-io/demarkus-library/internal/adapter/outbound/cache"
	"github.com/latebit-io/demarkus-library/internal/adapter/outbound/federated"
	"github.com/latebit-io/demarkus-library/internal/adapter/outbound/graphexport"
	"github.com/latebit-io/demarkus-library/internal/adapter/outbound/markdown"
	"github.com/latebit-io/demarkus-library/internal/adapter/outbound/oauth"
	"github.com/latebit-io/demarkus-library/internal/adapter/outbound/world"
	"github.com/latebit-io/demarkus-library/internal/core/port"
	"github.com/latebit-io/demarkus-library/internal/core/service"
	"github.com/latebit-io/demarkus/client/fetch"
	"github.com/latebit-io/nib/ai/llmconfig"
	niboauth "github.com/latebit-io/nib/ai/oauth"
)

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
	app.HTTPErrorHandler = web.HTMLErrorHandler
	app.Use(middleware.Recover())
	// Cap request bodies before any handler reads them (write POSTs).
	app.Use(middleware.BodyLimit(maxBodyBytes))
	// CSRF on every state-changing request (the cataloging-desk writes and the
	// login/logout forms). Cookie posture follows the session cookie's Secure
	// flag. Forms emit the token via {{ csrf }} (view.go).
	app.Use(web.CSRFMiddleware(config.CookieSecure))
	// Every page carries a Content-Security-Policy: branding stylesheets are
	// documents other people wrote, so what a stylesheet may fetch is bounded
	// here as well as at ingestion (csp.go).
	app.Use(web.SecurityHeaders())
	// Reader IP for the broker's per-IP limits (token exchange, refresh,
	// revoke); X-Real-IP is trusted verbatim, so this is opt-in.
	if config.ForwardReaderIP {
		app.Use(web.ReaderIP())
	}
	// Bound handler latency: a wedged outbound read returns 503 instead of
	// pinning the goroutine. Covers the turnstile's token refresh too. The
	// librarian paths are exempt; see web.LibrarianStreamPath.
	app.Use(middleware.ContextTimeoutWithConfig(middleware.ContextTimeoutConfig{
		Timeout: handlerTimeout,
		Skipper: func(c *echo.Context) bool {
			p := c.Request().URL.Path
			return p == web.LibrarianStreamPath || p == web.LibrarianAskPath
		},
	}))

	view, err := web.NewView()
	if err != nil {
		panic(err)
	}
	// Branding (the theme layer): the manifest file, with the single-value
	// environment knobs layered over it. Files were stat-checked at config
	// load; a read failure here (permissions, race) still stops startup loudly.
	branding, err := web.ThemeRoutes(app, themeManifest(config))
	if err != nil {
		logger.Error("branding invalid", "err", err)
		os.Exit(1)
	}
	app.Renderer = view.WithBranding(branding)

	web.StaticRoutes(app, config.StaticDir)
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
	// Theme assets stay public for the sign-in page, but a signed-in reader's
	// bearer rides along so a cold pod can read a private hub (#90).
	var themeBearer []echo.MiddlewareFunc
	// The broker publishes the hub's branding without a bearer, so a cold
	// pod and the sign-in page carry the room's identity (#90).
	var hubBranding *broker.PublicReader
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
		route, err := newBrokerRoute(logger, config)
		if err != nil {
			logger.Error("broker routing invalid", "err", err)
			os.Exit(1)
		}
		auth := brokerAuth{client: oauth.NewClient(oauth.Config{
			BrokerURL:    config.BrokerURL,
			ClientID:     config.ClientID,
			ClientSecret: config.ClientSecret,
			RedirectURI:  config.RedirectURI,
			Scopes:       config.Scopes,
			OnIssuerHost: issuerHostHook(route),
		}, routedClient(route, oauth.DefaultTimeout))}

		sealer, err := sessionSealer(logger, config.SessionKey)
		if err != nil {
			logger.Error("DEMARKUS_SESSION_KEY invalid", "err", err)
			os.Exit(1)
		}
		sessions := session.NewManager(sealer, auth, session.Config{TTL: config.SessionTTL})
		cookies := web.CookieConfig{Secure: config.CookieSecure}

		web.AuthRoutes(app, web.NewAuthHandler(auth, sessions, session.NewPendingLogins(sealer), cookies))
		turnstile = append(turnstile, web.RequireSession(sessions, cookies))
		themeBearer = append(themeBearer, web.SessionBearer(sessions))

		brokerHTTP := routedClient(route, broker.DefaultTimeout)
		bg := broker.NewGateway(config.BrokerURL, brokerHTTP)
		hubBranding = broker.NewPublicReader(config.BrokerURL, brokerHTTP)
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
		shutdown = func() {
			bg.Close()
			if fclient != nil {
				fclient.Close()
			}
		}
	}

	// Application core (the hexagon) + the reading room, same in both modes.
	// The rendered-document cache backs the trail engine's 1-read-per-click
	// budget (ADR 0005 decision 9).
	reading := service.NewReadingService(gateway, renderer, cache.NewMemory(0)).WithHub(config.Hub, graphexport.Parser{})
	// Phase 4 AI librarian (plans/phase-4-ai-librarian.md): nib-backed agent
	// over the core's read-only ports, joining the canvas as pane kind `a`.
	// Feature-dark unless nib's llmconfig resolves an LLM provider (global
	// llm.json or LLM_API_KEY/LLM_BASE_URL/LLM_MODEL) — without one the pane
	// reads "not on duty" and /a/stream serves only the ?slow= soak.
	lib := buildLibrarian(logger, reading, librarianSettings{
		defaultWorld: defaultWorld, keyStore: config.LLMKeyStore, asksPerHour: config.LibrarianAsksPerHour,
	})
	// In-world branding (ADR 0008): worlds brand themselves through documents
	// under /.well-known/library/, resolved ahead of the manifest's entries.
	// The hub world's documents brand the room as a whole, ahead of the files.
	brands := web.NewWorldBrands(reading).WithHub(config.Hub)
	if hubBranding != nil {
		brands = brands.WithHubSource(hubBranding)
	}
	view.WithWorldBrands(brands)
	web.WorldThemeRoutes(app, branding, brands, themeBearer...)
	room := web.NewRoom(reading, defaultWorld, config.DefaultDoc).WithBranding(branding).WithWorldBrands(brands)
	if lib != nil {
		room = room.WithLibrarian(lib)
	}
	if config.PaneScroll {
		room = room.WithPaneScroll()
	} else {
		logger.Info("page-scroll room selected (DEMARKUS_PANE_SCROLL=false)")
	}
	web.RoomRoutes(app, room, turnstile...)

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

// themeManifest loads the branding manifest (if any) and layers the
// environment knobs over it: env wins, so a one-off DEMARKUS_BRAND still
// works beside a manifest. Env asset paths are made absolute first — the
// manifest resolves relative paths against its own directory, which is the
// wrong base for a path typed at the shell.
func themeManifest(config *AppConfig) web.ThemeManifest {
	m, err := web.LoadThemeManifest(config.Branding)
	if err != nil {
		slog.Error("branding manifest invalid", "err", err)
		os.Exit(1)
	}
	if config.Brand != "" {
		m.Name = config.Brand
	}
	if config.Logo != "" {
		m.Logo = absPath(config.Logo)
	}
	if config.Favicon != "" {
		m.Favicon = absPath(config.Favicon)
	}
	if config.ThemeCSS != "" {
		m.CSS = absPath(config.ThemeCSS)
	}
	if config.TermUniverse != "" {
		m.Terms.Universe = config.TermUniverse
	}
	if config.TermLibrarian != "" {
		m.Terms.Librarian = config.TermLibrarian
	}
	return m
}

func absPath(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
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

// librarianSettings are the operator knobs the librarian is built with.
type librarianSettings struct {
	defaultWorld string
	keyStore     bool
	asksPerHour  int
}

// buildLibrarian resolves an LLM provider through nib's llmconfig and, when
// one is configured, assembles the librarian over the reading service's
// read-only port slices. Returns nil — the feature-dark posture (plan D6) —
// when no provider is configured; OAuth-profile auth is a nib-code concern
// and intentionally unsupported here (servers use API keys).
func buildLibrarian(logger *slog.Logger, reading *service.ReadingService, settings librarianSettings) port.Librarian {
	_, resolved := llmconfig.Resolve("")
	if resolved.OAuthProvider != "" {
		logger.Info("librarian disabled: OAuth LLM profiles are not supported server-side; configure an API-key profile")
		return nil
	}
	if !resolved.HasProvider() && settings.keyStore {
		// The key may live in nib's keystore (`nib` TUI-entered keys in
		// <UserConfigDir>/nib/keys.json) rather than the environment — the
		// same fallback nib's own binaries use. Env keys keep priority;
		// AppConfig (DEMARKUS_LLM_KEYSTORE=false) pins env-only. Failures
		// are logged, not swallowed: a corrupt store file should be seen.
		switch path, err := niboauth.DefaultKeyStorePath(); {
		case err != nil:
			logger.Warn("librarian: nib key store unavailable", "err", err)
		default:
			ks, err := niboauth.NewKeyStore(path)
			switch {
			case err != nil:
				logger.Warn("librarian: nib key store unreadable", "path", path, "err", err)
			case llmconfig.WireStoredKey(resolved, ks):
				logger.Info("librarian: API key loaded from nib key store", "profile", resolved.Profile)
			}
		}
	}
	if !resolved.HasProvider() {
		logger.Info("librarian disabled: no LLM provider configured")
		return nil
	}
	provider := resolved.NewProvider()
	if provider == nil {
		logger.Warn("librarian disabled: provider construction failed", "profile", resolved.Profile)
		return nil
	}
	lib, err := librarian.New(librarian.Config{
		Provider:     provider,
		Reader:       reading,
		Catalog:      reading,
		Graph:        reading,
		Map:          reading,
		DefaultWorld: settings.defaultWorld,
		Logger:       logger,
		AsksPerHour:  settings.asksPerHour,
	})
	if err != nil {
		logger.Warn("librarian disabled", "err", err)
		return nil
	}
	logger.Info("librarian enabled", "profile", resolved.Profile, "model", resolved.DisplayModel())
	return lib
}

// sessionSealer seals cookies under the configured key. Without one it mints
// a per-process key: sessions then end at restart and never cross replicas.
func sessionSealer(logger *slog.Logger, key []byte) (*session.Sealer, error) {
	if len(key) == 0 {
		logger.Warn("DEMARKUS_SESSION_KEY unset: sessions sealed with a per-process key end at restart and do not cross replicas")
		return session.NewEphemeralSealer(), nil
	}
	return session.NewSealer(key)
}

// worldLabel names the world being served for the startup log line.
func worldLabel(config *AppConfig) string {
	if config.Transport == TransportBroker {
		return config.World + " via " + config.BrokerURL
	}
	return config.Host
}
