package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/latebit-io/demarkus-library/internal/adapter/outbound/brokerroute"
)

// brokerRoute holds the HTTP clients for server-side broker calls. With no
// internal URL the clients are nil and the adapters use their defaults.
type brokerRoute struct {
	oauthClient   *http.Client
	gatewayClient *http.Client
	issuerHost    func(host string)
}

func newBrokerRoute(logger *slog.Logger, config *AppConfig) (brokerRoute, error) {
	if config.BrokerInternalURL == "" {
		return brokerRoute{}, nil
	}
	internal, err := brokerroute.ParseInternalURL(config.BrokerInternalURL)
	if err != nil {
		return brokerRoute{}, err
	}
	transport, err := brokerroute.New(internal, config.BrokerURL, nil)
	if err != nil {
		return brokerRoute{}, fmt.Errorf("broker routing: %w", err)
	}
	if internal.Scheme == "http" {
		logger.Warn("broker internal URL is plain http: client secret and tokens cross the cluster unencrypted")
	}
	logger.Info("broker calls routed internally", "public", config.BrokerURL, "internal", internal.String())
	// Timeouts match the adapters' own defaults.
	return brokerRoute{
		oauthClient:   &http.Client{Transport: transport, Timeout: 10 * time.Second},
		gatewayClient: &http.Client{Transport: transport, Timeout: 15 * time.Second},
		issuerHost:    transport.AddHost,
	}, nil
}
