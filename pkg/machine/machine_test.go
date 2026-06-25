package machine

import (
	"os"
	"testing"
)

func TestSetEnvDefault(t *testing.T) {
	const key = "DEPOT_TEST_SET_ENV_DEFAULT"

	t.Run("sets the value when unset", func(t *testing.T) {
		os.Unsetenv(key)
		t.Cleanup(func() { os.Unsetenv(key) })

		setEnvDefault(key, "default")

		if got := os.Getenv(key); got != "default" {
			t.Fatalf("expected %q, got %q", "default", got)
		}
	})

	t.Run("preserves an operator-provided value", func(t *testing.T) {
		t.Setenv(key, "operator")

		setEnvDefault(key, "default")

		if got := os.Getenv(key); got != "operator" {
			t.Fatalf("expected operator value to be preserved, got %q", got)
		}
	})
}

func TestApplyBuildKitKeepaliveDefaults(t *testing.T) {
	os.Unsetenv("DEPOT_KEEPALIVE_CLIENT_TIME_MS")
	os.Unsetenv("DEPOT_KEEPALIVE_CLIENT_TIMEOUT_MS")
	t.Cleanup(func() {
		os.Unsetenv("DEPOT_KEEPALIVE_CLIENT_TIME_MS")
		os.Unsetenv("DEPOT_KEEPALIVE_CLIENT_TIMEOUT_MS")
	})

	applyBuildKitKeepaliveDefaults()

	if got := os.Getenv("DEPOT_KEEPALIVE_CLIENT_TIME_MS"); got != "30000" {
		t.Fatalf("expected keepalive time default 30000, got %q", got)
	}
	if got := os.Getenv("DEPOT_KEEPALIVE_CLIENT_TIMEOUT_MS"); got != "10000" {
		t.Fatalf("expected keepalive timeout default 10000, got %q", got)
	}
}
