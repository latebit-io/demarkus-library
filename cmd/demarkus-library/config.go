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
	cfg := &AppConfig{
		Port:      getEnvAsInt("PORT", 8080),
		Transport: getEnv("DEMARKUS_TRANSPORT", TransportQUIC),

		Host:       getEnv("DEMARKUS_HOST", "soul.demarkus.io"),
		ReadToken:  getEnv("DEMARKUS_AUTH", ""),
		Insecure:   getEnvAsBool("DEMARKUS_INSECURE", true),
		DefaultDoc: getEnv("DEMARKUS_DEFAULT_DOC", "/index.md"),

		BrokerURL:    getEnv("DEMARKUS_BROKER_URL", ""),
		ClientID:     getEnv("DEMARKUS_CLIENT_ID", ""),
		ClientSecret: getEnv("DEMARKUS_CLIENT_SECRET", ""),
		RedirectURI:  getEnv("DEMARKUS_REDIRECT_URI", ""),
		World:        getEnv("DEMARKUS_WORLD", ""),
		Scopes:       strings.Fields(getEnv("DEMARKUS_SCOPES", "mark.read")),
		SessionTTL:   getEnvAsDuration("DEMARKUS_SESSION_TTL", 720*time.Hour),
		CookieSecure: getEnvAsBool("DEMARKUS_COOKIE_SECURE", true),
	}

	switch cfg.Transport {
	case TransportQUIC:
		// Phase 0/1a defaults are complete.
	case TransportBroker:
		var missing []string
		for _, kv := range []struct{ key, val string }{
			{"DEMARKUS_BROKER_URL", cfg.BrokerURL},
			{"DEMARKUS_CLIENT_ID", cfg.ClientID},
			{"DEMARKUS_CLIENT_SECRET", cfg.ClientSecret},
			{"DEMARKUS_REDIRECT_URI", cfg.RedirectURI},
			{"DEMARKUS_WORLD", cfg.World},
		} {
			if kv.val == "" {
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

func getEnvAsDuration(key string, fallback time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
