package transport

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	APIKey           string
	AttemptTimeout   time.Duration
	InitialBackoff   time.Duration
	OpenTimeout      time.Duration
	FailureWindow    time.Duration
	FailureThreshold uint32
}

func DefaultConfig() Config {
	return Config{
		AttemptTimeout:   2 * time.Second,
		InitialBackoff:   100 * time.Millisecond,
		OpenTimeout:      15 * time.Second,
		FailureWindow:    30 * time.Second,
		FailureThreshold: 5,
	}
}

func ConfigFromEnv() (Config, error) {
	cfg := DefaultConfig()
	cfg.APIKey = os.Getenv("FLIGHT_API_KEY")
	for _, field := range []struct {
		name  string
		value *time.Duration
	}{
		{"GRPC_ATTEMPT_TIMEOUT", &cfg.AttemptTimeout},
		{"GRPC_INITIAL_BACKOFF", &cfg.InitialBackoff},
		{"CIRCUIT_OPEN_TIMEOUT", &cfg.OpenTimeout},
		{"CIRCUIT_FAILURE_WINDOW", &cfg.FailureWindow},
	} {
		if raw := os.Getenv(field.name); raw != "" {
			value, err := time.ParseDuration(raw)
			if err != nil {
				return Config{}, fmt.Errorf("%s: %w", field.name, err)
			}
			*field.value = value
		}
	}
	if raw := os.Getenv("CIRCUIT_FAILURE_THRESHOLD"); raw != "" {
		value, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return Config{}, fmt.Errorf("CIRCUIT_FAILURE_THRESHOLD: %w", err)
		}
		cfg.FailureThreshold = uint32(value)
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (cfg Config) validate() error {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return fmt.Errorf("FLIGHT_API_KEY must be nonempty")
	}
	for _, character := range cfg.APIKey {
		if character < 0x20 || character > 0x7e {
			return fmt.Errorf("FLIGHT_API_KEY must contain printable ASCII characters")
		}
	}
	for _, field := range []struct {
		name  string
		value time.Duration
	}{
		{"GRPC_ATTEMPT_TIMEOUT", cfg.AttemptTimeout},
		{"GRPC_INITIAL_BACKOFF", cfg.InitialBackoff},
		{"CIRCUIT_OPEN_TIMEOUT", cfg.OpenTimeout},
		{"CIRCUIT_FAILURE_WINDOW", cfg.FailureWindow},
	} {
		if field.value <= 0 {
			return fmt.Errorf("%s must be positive", field.name)
		}
	}
	if cfg.InitialBackoff > time.Duration(1<<63-1)/2 {
		return fmt.Errorf("GRPC_INITIAL_BACKOFF is too large to double")
	}
	if cfg.FailureThreshold == 0 {
		return fmt.Errorf("CIRCUIT_FAILURE_THRESHOLD must be positive")
	}
	return nil
}
