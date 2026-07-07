package config

import (
	"testing"
)

func TestLoad_Defaults(t *testing.T) {
	// Clear any env that might be set from the shell running the test.
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("LOG_FORMAT", "")
	t.Setenv("METRICS_ADDR", "")

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
	if cfg.Observability.MetricsAddr != ":8080" {
		t.Errorf("MetricsAddr default: got %q, want :8080", cfg.Observability.MetricsAddr)
	}
}

func TestLoad_EnvOverrides(t *testing.T) {
	t.Setenv("LOG_LEVEL", "DEBUG")
	t.Setenv("LOG_FORMAT", "text")
	t.Setenv("METRICS_ADDR", ":9090")

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
	if cfg.Observability.MetricsAddr != ":9090" {
		t.Errorf("MetricsAddr: got %q, want :9090", cfg.Observability.MetricsAddr)
	}
}
