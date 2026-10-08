package main

import (
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestNewBrokerRoute(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cases := []struct {
		name, internal string
		wantRoute      bool
		wantErr        string
	}{
		{name: "unset", internal: "", wantRoute: false},
		{name: "origin", internal: "http://broker.ns.svc.cluster.local", wantRoute: true},
		{name: "path rejected", internal: "http://broker.ns.svc/mcp", wantErr: "DEMARKUS_BROKER_INTERNAL_URL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &AppConfig{BrokerURL: "https://broker.example.org", BrokerInternalURL: tc.internal}
			route, err := newBrokerRoute(logger, cfg)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to name %s", err, tc.wantErr)
				}
				return
			}
			if err != nil || (route != nil) != tc.wantRoute {
				t.Fatalf("route=%v err=%v, want route %v", route, err, tc.wantRoute)
			}
			// Nil route must propagate as nil client and hook so the adapters
			// fall back to their defaults.
			if (routedClient(route, 0) != nil) != tc.wantRoute || (issuerHostHook(route) != nil) != tc.wantRoute {
				t.Fatal("client and hook must follow the route's presence")
			}
		})
	}
}
