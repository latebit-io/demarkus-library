package main

import (
	"os"
	"strconv"
)

// AppConfig is the front-end's runtime configuration, loaded from the
// environment (mirrors the bulwarkauth config pattern).
type AppConfig struct {
	Port       int    // HTTP listen port
	Host       string // demarkus world host (host[:port])
	DefaultDoc string // document served at /
	ReadToken  string // read token for private paths (empty for public worlds)
	Insecure   bool   // skip TLS verification (dev worlds use self-signed certs)
}

// NewAppConfig reads configuration from the environment with Phase 0 defaults
// that point at a dev/soul world directly over QUIC.
func NewAppConfig() (*AppConfig, error) {
	return &AppConfig{
		Port:       getEnvAsInt("PORT", 8080),
		Host:       getEnv("DEMARKUS_HOST", "soul.demarkus.io"),
		DefaultDoc: getEnv("DEMARKUS_DEFAULT_DOC", "/index.md"),
		ReadToken:  getEnv("DEMARKUS_AUTH", ""),
		Insecure:   getEnvAsBool("DEMARKUS_INSECURE", true),
	}, nil
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
