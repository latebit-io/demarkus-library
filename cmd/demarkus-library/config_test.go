package main

import (
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
