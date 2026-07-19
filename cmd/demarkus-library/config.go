package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Transport selects the outbound world adapter at the composition root.
const (
	// TransportQUIC reads one world directly over the demarkus QUIC fetch
	// client — the Phase 0/1a path, auth-free against a dev/soul world.
	TransportQUIC = "quic"
	// TransportBroker reads through the broker's MCP gateway behind the
	// org-login turnstile — the Phase 1b path (ADR 0004).
	TransportBroker = "broker"
)

// AppConfig is the front-end's runtime configuration, loaded from the
// environment (mirrors the bulwarkauth config pattern).
type AppConfig struct {
	Port      int    // HTTP listen port
	Transport string // "quic" (direct world) or "broker" (MCP gateway + login)

	// Federation allows following mark:// links to demarkus servers outside
	// the home world / knowledge system — direct, tokenless QUIC reads of
	// whatever host the link names. This is the distributed knowledge graph
	// working as intended; turn it off to pin readers to the home worlds.
	Federation bool

	// Hub names the world whose published topology documents (the federation
	// agent's hash index + graph export) are the floor's durable universe map
	// (decision 11; plans "Floor enrichment"). Empty ⇒ no hub: the floor falls
	// back to mark_worlds + per-world lookup. In quic mode it defaults to the
	// home world (the world can be its own hub); in broker mode it is set
	// explicitly (the cluster's fixed-name `root`).
	Hub string

	// PaneScroll runs the reading room as the pane-scroll room (ADR 0007,
	// the experiment's verdict): a fixed-viewport canvas where each pane
	// scrolls internally, the margin is summoned via the ?meta= lens, and
	// reading columns share the screen. Default ON — the flag is now the
	// opt-out escape hatch back to the page-scroll room, kept until that
	// room is retired. Presentation only, no route or state change.
	PaneScroll bool

	// LLMKeyStore lets the librarian fall back to nib's on-disk key store
	// (<UserConfigDir>/nib/keys.json) when no LLM key arrives via the
	// environment — the dev-machine convenience that makes a TUI-entered key
	// (e.g. fugu) just work. Environment keys always win; cluster
	// deployments provide the key via Secret-mounted env and the store file
	// simply doesn't exist there. Set false to pin the composition root to
	// env-only LLM config.
	LLMKeyStore bool

	// Branding lets an operator present the library as their own room:
	// Brand replaces the "demarkus Library" wordmark in titles, nav, and the
	// login card; Logo is a path to an image file shown beside it; ThemeCSS
	// is a path to a stylesheet served after the built-in styles, where a
	// deployment overrides the design tokens (--paper, --ink, --font-prose,
	// the signal colors, …) or any rule. All optional; unset keeps the
	// stock room.
	Brand    string // display name (default "demarkus Library")
	Logo     string // path to a logo image file (empty ⇒ none)
	ThemeCSS string // path to an override stylesheet (empty ⇒ none)

	// TLS serves the library itself over HTTPS when both are set. In the
	// cluster the ingress terminates TLS and these stay empty; locally they
	// let the broker's https-only redirect rule be satisfied without a
	// reverse proxy (mkcert + an /etc/hosts name — see the deploy notes).
	TLSCert string // path to the certificate (PEM)
	TLSKey  string // path to the private key (PEM)

	// Direct-QUIC mode (Phase 0/1a).
	Host       string // demarkus world host (host[:port])
	ReadToken  string // read token for private paths (empty for public worlds)
	Insecure   bool   // skip TLS verification (dev worlds use self-signed certs)
	DefaultDoc string // document served at /

	// Broker mode (Phase 1b) — the library is a registered confidential
	// web client at the broker.
	BrokerURL    string        // broker origin, e.g. https://broker.example.org
	ClientID     string        // webClients registry entry
	ClientSecret string        // plaintext secret (broker stores the sha256)
	RedirectURI  string        // must exactly match a registered redirect URI
	World        string        // world name for mark://<world>/<path> reads
	Scopes       []string      // OAuth scopes (default mark.read)
	SessionTTL   time.Duration // absolute session lifetime (default 720h)
	CookieSecure bool          // Secure flag on the session cookie (false only for localhost dev)
}

// NewAppConfig reads configuration from the environment. Defaults keep the
// Phase 0/1a demo path working: direct QUIC against a dev/soul world. Broker
// mode validates its required settings loudly at startup.
func NewAppConfig() (*AppConfig, error) {
	sessionTTL, err := getEnvAsDuration("DEMARKUS_SESSION_TTL", 720*time.Hour)
	if err != nil {
		return nil, err
	}
	// Federation defaults ON — following links across the distributed graph
	// is the product, and federated reads are anonymous and tokenless (the
	// browser-following-links posture). The parse is strict so a mistyped
	// value stops startup rather than silently picking either side of a
	// security-relevant switch. Operators who want fail-closed set false.
	federation, err := getEnvAsBoolStrict("DEMARKUS_FEDERATION", true)
	if err != nil {
		return nil, err
	}
	// Strict parse so a typo'd value stops startup instead of silently
	// picking a room (the switch decides the whole canvas presentation).
	paneScroll, err := getEnvAsBoolStrict("DEMARKUS_PANE_SCROLL", true)
	if err != nil {
		return nil, err
	}
	llmKeyStore, err := getEnvAsBoolStrict("DEMARKUS_LLM_KEYSTORE", true)
	if err != nil {
		return nil, err
	}
	if sessionTTL <= 0 {
		// A non-positive TTL would mint sessions that expire on arrival —
		// a confusing login loop instead of a clear startup failure.
		return nil, fmt.Errorf("DEMARKUS_SESSION_TTL must be positive, got %s", sessionTTL)
	}

	cfg := &AppConfig{
		Port:        getEnvAsInt("PORT", 8080),
		Transport:   getEnv("DEMARKUS_TRANSPORT", TransportQUIC),
		Federation:  federation,
		PaneScroll:  paneScroll,
		LLMKeyStore: llmKeyStore,

		// Trimmed so whitespace-only values stay unset instead of
		// flipping the server into TLS mode and failing on file open.
		TLSCert: strings.TrimSpace(getEnv("DEMARKUS_TLS_CERT", "")),
		TLSKey:  strings.TrimSpace(getEnv("DEMARKUS_TLS_KEY", "")),

		// Blank (or whitespace) brand falls back to the stock wordmark —
		// an empty nav brand would leave the room unnamed.
		Brand:    getEnv("DEMARKUS_BRAND", ""),
		Logo:     strings.TrimSpace(getEnv("DEMARKUS_LOGO", "")),
		ThemeCSS: strings.TrimSpace(getEnv("DEMARKUS_THEME_CSS", "")),

		Host:       getEnv("DEMARKUS_HOST", "soul.demarkus.io"),
		ReadToken:  getEnv("DEMARKUS_AUTH", ""),
		Insecure:   getEnvAsBool("DEMARKUS_INSECURE", true),
		DefaultDoc: getEnv("DEMARKUS_DEFAULT_DOC", "/index.md"),
		Hub:        strings.TrimSpace(getEnv("DEMARKUS_HUB", "")),

		BrokerURL:    getEnv("DEMARKUS_BROKER_URL", ""),
		ClientID:     getEnv("DEMARKUS_CLIENT_ID", ""),
		ClientSecret: getEnv("DEMARKUS_CLIENT_SECRET", ""),
		RedirectURI:  getEnv("DEMARKUS_REDIRECT_URI", ""),
		World:        getEnv("DEMARKUS_WORLD", ""),
		Scopes:       strings.Fields(getEnv("DEMARKUS_SCOPES", "mark.read")),
		SessionTTL:   sessionTTL,
		CookieSecure: getEnvAsBool("DEMARKUS_COOKIE_SECURE", true),
	}

	if (cfg.TLSCert == "") != (cfg.TLSKey == "") {
		return nil, fmt.Errorf("DEMARKUS_TLS_CERT and DEMARKUS_TLS_KEY must be set together")
	}
	if strings.TrimSpace(cfg.Brand) == "" {
		cfg.Brand = "demarkus Library"
	}
	// Branding assets must exist at startup — a typo'd path would otherwise
	// surface as a broken logo or an unstyled room on first request.
	for _, kv := range []struct{ key, path string }{
		{"DEMARKUS_LOGO", cfg.Logo},
		{"DEMARKUS_THEME_CSS", cfg.ThemeCSS},
	} {
		if kv.path == "" {
			continue
		}
		if _, err := os.Stat(kv.path); err != nil {
			return nil, fmt.Errorf("%s: %w", kv.key, err)
		}
	}

	switch cfg.Transport {
	case TransportQUIC:
		// Phase 0/1a defaults are complete. The home world can be its own
		// hub: default DEMARKUS_HUB to it so a single-world deployment reads
		// its own published topology (if an agent put one there) without extra
		// config. An operator points elsewhere by setting DEMARKUS_HUB.
		if cfg.Hub == "" {
			cfg.Hub = cfg.Host
		}
	case TransportBroker:
		var missing []string
		for _, kv := range []struct{ key, val string }{
			{"DEMARKUS_BROKER_URL", cfg.BrokerURL},
			{"DEMARKUS_CLIENT_ID", cfg.ClientID},
			{"DEMARKUS_CLIENT_SECRET", cfg.ClientSecret},
			{"DEMARKUS_REDIRECT_URI", cfg.RedirectURI},
			{"DEMARKUS_WORLD", cfg.World},
		} {
			// Whitespace-only counts as missing — it would pass here
			// only to fail opaquely in the OAuth dance later.
			if strings.TrimSpace(kv.val) == "" {
				missing = append(missing, kv.key)
			}
		}
		if len(missing) > 0 {
			return nil, fmt.Errorf("broker transport requires %s", strings.Join(missing, ", "))
		}
	default:
		return nil, fmt.Errorf("DEMARKUS_TRANSPORT must be %q or %q, got %q",
			TransportQUIC, TransportBroker, cfg.Transport)
	}
	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func getEnvAsInt(key string, fallback int) int {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func getEnvAsBool(key string, fallback bool) bool {
	if v, ok := os.LookupEnv(key); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}

// getEnvAsBoolStrict surfaces a parse error instead of silently falling
// back — for switches where either silent direction is wrong (federation
// gates server-side outbound dials).
func getEnvAsBoolStrict(key string, fallback bool) (bool, error) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return fallback, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s: invalid bool %q: %w", key, v, err)
	}
	return b, nil
}

// getEnvAsDuration parses key as a Go duration. Unlike the int/bool helpers
// it surfaces a parse error rather than silently falling back: a mistyped
// session TTL must stop startup, not quietly become 720h.
func getEnvAsDuration(key string, fallback time.Duration) (time.Duration, error) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid duration %q: %w", key, v, err)
	}
	return d, nil
}
