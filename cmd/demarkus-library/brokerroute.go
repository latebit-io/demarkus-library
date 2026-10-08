package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/latebit-io/demarkus-library/internal/adapter/outbound/brokerroute"
)

// newBrokerRoute builds the transport for DEMARKUS_BROKER_INTERNAL_URL. Nil
// when unset: the adapters then use their default clients.
func newBrokerRoute(logger *slog.Logger, config *AppConfig) (*brokerroute.Transport, error) {
	if config.BrokerInternalURL == "" {
		return nil, nil
	}
	internal, err := brokerroute.ParseInternalURL(config.BrokerInternalURL)
	if err != nil {
		return nil, fmt.Errorf("DEMARKUS_BROKER_INTERNAL_URL: %w", err)
	}
	transport, err := brokerroute.New(internal, config.BrokerURL)
	if err != nil {
		return nil, fmt.Errorf("broker routing: %w", err)
	}
	if internal.Scheme == "http" {
		logger.Warn("broker internal URL is plain http: client secret and tokens cross the cluster unencrypted")
	}
	logger.Info("broker calls routed internally", "public", config.BrokerURL, "internal", internal.String())
	return transport, nil
}

// routedClient wraps the transport with an adapter's timeout; nil transport
// yields nil so the adapter falls back to its default client.
func routedClient(transport *brokerroute.Transport, timeout time.Duration) *http.Client {
	if transport == nil {
		return nil
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}

// issuerHostHook returns the discovery hook that teaches the transport the
// advertised issuer host, or nil when nothing is routed.
func issuerHostHook(transport *brokerroute.Transport) func(string) {
	if transport == nil {
		return nil
	}
	return transport.AddHost
}
