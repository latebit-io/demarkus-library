package main

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// These lock the fail-fast contract: a malformed value for a load-bearing switch
// must stop startup, not silently become the fallback (the comments on the
// helpers promise exactly this, and it was previously untested).

func TestGetEnvAsBoolStrict(t *testing.T) {
	if v, err := getEnvAsBoolStrict("DL_TEST_BOOL_UNSET", true); err != nil || !v {
		t.Fatalf("unset = (%v,%v), want (true,nil)", v, err)
	}
	t.Setenv("DL_TEST_BOOL", "nope")
	if _, err := getEnvAsBoolStrict("DL_TEST_BOOL", true); err == nil {
		t.Fatal("invalid bool must error (fail startup), not fall back")
	}
	t.Setenv("DL_TEST_BOOL", "false")
	if v, err := getEnvAsBoolStrict("DL_TEST_BOOL", true); err != nil || v {
		t.Fatalf("valid = (%v,%v), want (false,nil)", v, err)
	}
}

func TestGetEnvAsDuration(t *testing.T) {
	if d, err := getEnvAsDuration("DL_TEST_DUR_UNSET", time.Hour); err != nil || d != time.Hour {
		t.Fatalf("unset = (%v,%v), want (1h,nil)", d, err)
	}
	t.Setenv("DL_TEST_DUR", "ninety")
	if _, err := getEnvAsDuration("DL_TEST_DUR", time.Hour); err == nil {
		t.Fatal("invalid duration must error (fail startup), not silently become the fallback")
	}
	t.Setenv("DL_TEST_DUR", "45m")
	if d, err := getEnvAsDuration("DL_TEST_DUR", time.Hour); err != nil || d != 45*time.Minute {
		t.Fatalf("valid = (%v,%v), want (45m,nil)", d, err)
	}
}

// A malformed strict switch propagates out of NewAppConfig as a startup error.
func TestNewAppConfigRejectsBadFederation(t *testing.T) {
	t.Setenv("DEMARKUS_FEDERATION", "maybe")
	_, err := NewAppConfig()
	if err == nil {
		t.Fatal("a malformed DEMARKUS_FEDERATION must stop startup")
	}
	// Assert it's the federation parse that failed, not some unrelated config
	// error — otherwise the test could pass for the wrong reason.
	if !strings.Contains(err.Error(), "DEMARKUS_FEDERATION") {
		t.Fatalf("expected a DEMARKUS_FEDERATION parse error, got: %v", err)
	}
}

func TestNewAppConfigLLMKeyStore(t *testing.T) {
	// Defaults on (the dev-machine convenience); malformed stops startup.
	cfg, err := NewAppConfig()
	if err != nil {
		t.Fatalf("NewAppConfig: %v", err)
	}
	if !cfg.LLMKeyStore {
		t.Error("LLMKeyStore should default true")
	}
	t.Setenv("DEMARKUS_LLM_KEYSTORE", "definitely")
	if _, err := NewAppConfig(); err == nil || !strings.Contains(err.Error(), "DEMARKUS_LLM_KEYSTORE") {
		t.Fatalf("expected a DEMARKUS_LLM_KEYSTORE parse error, got: %v", err)
	}
	t.Setenv("DEMARKUS_LLM_KEYSTORE", "false")
	cfg, err = NewAppConfig()
	if err != nil {
		t.Fatalf("NewAppConfig: %v", err)
	}
	if cfg.LLMKeyStore {
		t.Error("DEMARKUS_LLM_KEYSTORE=false should pin env-only")
	}
}

func TestNewAppConfigPaneScrollDefaultsOn(t *testing.T) {
	// ADR 0007: the pane-scroll room is the default; the env is the opt-out.
	cfg, err := NewAppConfig()
	if err != nil {
		t.Fatalf("NewAppConfig: %v", err)
	}
	if !cfg.PaneScroll {
		t.Error("PaneScroll should default true (ADR 0007)")
	}
	t.Setenv("DEMARKUS_PANE_SCROLL", "false")
	if cfg, err = NewAppConfig(); err != nil || cfg.PaneScroll {
		t.Errorf("DEMARKUS_PANE_SCROLL=false should select the page-scroll room (err %v)", err)
	}
}

func TestNewAppConfigLibrarianAsksPerHour(t *testing.T) {
	cfg, err := NewAppConfig()
	if err != nil || cfg.LibrarianAsksPerHour != 0 {
		t.Fatalf("unset: %d, %v; want 0 (the librarian's default)", cfg.LibrarianAsksPerHour, err)
	}
	t.Setenv("DEMARKUS_LIBRARIAN_ASKS_PER_HOUR", "12")
	if cfg, err = NewAppConfig(); err != nil || cfg.LibrarianAsksPerHour != 12 {
		t.Errorf("12: %d, %v", cfg.LibrarianAsksPerHour, err)
	}
	for _, bad := range []string{"0", "-3", "lots"} {
		t.Setenv("DEMARKUS_LIBRARIAN_ASKS_PER_HOUR", bad)
		if _, err := NewAppConfig(); err == nil {
			t.Errorf("%q accepted; a typo must not lift the cost guard", bad)
		}
	}
}

// A mistyped sealing key must stop startup: replicas on different keys would
// bounce every reader whose cookie another one sealed. Length is the sealer's.
func TestParseSessionKey(t *testing.T) {
	key := bytes.Repeat([]byte{0xfb}, 32)
	tests := []struct {
		name    string
		raw     string
		want    []byte
		wantErr bool
	}{
		{name: "unset", raw: "  "},
		{name: "standard", raw: base64.StdEncoding.EncodeToString(key), want: key},
		{name: "url alphabet", raw: base64.URLEncoding.EncodeToString(key), wantErr: true},
		{name: "not base64", raw: "not a key!", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSessionKey(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !bytes.Equal(got, tt.want) {
				t.Errorf("key = %x, want %x", got, tt.want)
			}
		})
	}
}
