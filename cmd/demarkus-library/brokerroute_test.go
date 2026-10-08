package main

import (
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestNewBrokerRoute(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &AppConfig{BrokerURL: "https://broker.example.org"}

	route, err := newBrokerRoute(logger, cfg)
	if err != nil || route != nil {
		t.Fatalf("unset: route=%v err=%v, want nil, nil", route, err)
	}
	if routedClient(route, 0) != nil || issuerHostHook(route) != nil {
		t.Fatal("unset route must yield nil client and hook")
	}

	cfg.BrokerInternalURL = "http://broker.ns.svc.cluster.local"
	if route, err = newBrokerRoute(logger, cfg); err != nil || route == nil {
		t.Fatalf("valid internal URL: route=%v err=%v", route, err)
	}

	cfg.BrokerInternalURL = "http://broker.ns.svc/mcp"
	if _, err = newBrokerRoute(logger, cfg); err == nil || !strings.Contains(err.Error(), "DEMARKUS_BROKER_INTERNAL_URL") {
		t.Fatalf("path in internal URL must fail naming the var, got %v", err)
	}
}
