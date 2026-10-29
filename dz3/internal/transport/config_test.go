package transport

import (
	"strings"
	"testing"
	"time"
)

func cleanConfigEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"FLIGHT_API_KEY", "GRPC_ATTEMPT_TIMEOUT", "GRPC_INITIAL_BACKOFF", "CIRCUIT_OPEN_TIMEOUT", "CIRCUIT_FAILURE_WINDOW", "CIRCUIT_FAILURE_THRESHOLD"} {
		t.Setenv(name, "")
	}
}

func TestConfigFromEnvRejectsMissingAPIKey(t *testing.T) {
	cleanConfigEnv(t)
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("missing credentials accepted")
	}
}

func TestConfigFromEnvLoadsOverrides(t *testing.T) {
	cleanConfigEnv(t)
	t.Setenv("FLIGHT_API_KEY", "test-key")
	t.Setenv("GRPC_ATTEMPT_TIMEOUT", "250ms")
	t.Setenv("GRPC_INITIAL_BACKOFF", "10ms")
	t.Setenv("CIRCUIT_OPEN_TIMEOUT", "2s")
	t.Setenv("CIRCUIT_FAILURE_WINDOW", "7s")
	t.Setenv("CIRCUIT_FAILURE_THRESHOLD", "3")
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != "test-key" || cfg.AttemptTimeout != 250*time.Millisecond || cfg.InitialBackoff != 10*time.Millisecond || cfg.OpenTimeout != 2*time.Second || cfg.FailureWindow != 7*time.Second || cfg.FailureThreshold != 3 {
		t.Fatalf("incorrect environment configuration: %+v", cfg)
	}
}

func TestConfigFromEnvRejectsInvalidValues(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"FLIGHT_API_KEY", "   "},
		{"FLIGHT_API_KEY", "bad\nkey"},
		{"GRPC_ATTEMPT_TIMEOUT", "nonsense"},
		{"GRPC_ATTEMPT_TIMEOUT", "0s"},
		{"GRPC_INITIAL_BACKOFF", "-1s"},
		{"GRPC_INITIAL_BACKOFF", "2562047h47m16s"},
		{"CIRCUIT_OPEN_TIMEOUT", "0s"},
		{"CIRCUIT_FAILURE_WINDOW", "-1s"},
		{"CIRCUIT_FAILURE_THRESHOLD", "0"},
		{"CIRCUIT_FAILURE_THRESHOLD", "-1"},
		{"CIRCUIT_FAILURE_THRESHOLD", "4294967296"},
	} {
		t.Run(tc.name+"/"+tc.value, func(t *testing.T) {
			cleanConfigEnv(t)
			t.Setenv("FLIGHT_API_KEY", "secret")
			t.Setenv(tc.name, tc.value)
			_, err := ConfigFromEnv()
			if err == nil || !strings.Contains(err.Error(), tc.name) {
				t.Fatalf("expected configuration error for %s, got %v", tc.name, err)
			}
		})
	}
}

func TestNewClientInterceptorRejectsInvalidConfig(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Config)
	}{
		{"key", func(c *Config) { c.APIKey = "" }},
		{"timeout", func(c *Config) { c.AttemptTimeout = 0 }},
		{"backoff", func(c *Config) { c.InitialBackoff = -time.Second }},
		{"open", func(c *Config) { c.OpenTimeout = 0 }},
		{"window", func(c *Config) { c.FailureWindow = 0 }},
		{"threshold", func(c *Config) { c.FailureThreshold = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig()
			tc.change(&cfg)
			if _, err := NewClientInterceptor(cfg, nil); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}
