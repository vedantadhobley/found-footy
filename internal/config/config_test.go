package config

import (
	"testing"
)

func TestLoad_Defaults(t *testing.T) {
	// Clear any env that might be set from the shell running the test.
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("LOG_FORMAT", "")
	t.Setenv("LOKI_ENABLED", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Observability.LogLevel != "INFO" {
		t.Errorf("LogLevel default: got %q, want INFO", cfg.Observability.LogLevel)
	}
	if cfg.Observability.LogFormat != "json" {
		t.Errorf("LogFormat default: got %q, want json", cfg.Observability.LogFormat)
	}
	if !cfg.Observability.LokiEnabled {
		t.Errorf("LokiEnabled default: got false, want true")
	}
}

func TestLoad_EnvOverrides(t *testing.T) {
	t.Setenv("LOG_LEVEL", "DEBUG")
	t.Setenv("LOG_FORMAT", "text")
	t.Setenv("LOKI_ENABLED", "false")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Observability.LogLevel != "DEBUG" {
		t.Errorf("LogLevel: got %q, want DEBUG", cfg.Observability.LogLevel)
	}
	if cfg.Observability.LogFormat != "text" {
		t.Errorf("LogFormat: got %q, want text", cfg.Observability.LogFormat)
	}
	if cfg.Observability.LokiEnabled {
		t.Errorf("LokiEnabled: got true, want false")
	}
}

func TestLoad_InvalidBool(t *testing.T) {
	t.Setenv("LOKI_ENABLED", "not-a-bool")

	_, err := Load()
	if err == nil {
		t.Fatalf("Load should have returned error for invalid bool")
	}
}
