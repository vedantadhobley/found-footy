// ObservabilityConfig — env-driven settings for logging + /metrics.
package config

// ObservabilityConfig covers the log level + format that the
// internal/observability/logging package reads at startup.
//
// Additional observability config (metrics endpoint, tracing endpoint)
// gets added here as those packages become non-stub.
type ObservabilityConfig struct {
	// LogLevel is one of "DEBUG", "INFO", "WARN", "ERROR". Case-insensitive.
	// Default INFO — production-safe verbosity.
	LogLevel string `env:"LOG_LEVEL" envDefault:"INFO"`

	// LogFormat is "json" (production; ingested by Loki) or "text"
	// (dev-friendly, human-readable).
	// Default json — matches what Promtail expects.
	LogFormat string `env:"LOG_FORMAT" envDefault:"json"`

	// MetricsAddr is the listen address for the Prometheus /metrics
	// endpoint that every binary exposes. Defaults to :8080 — each
	// docker-compose service can override via env if needed. The API read
	// surface uses a separate address.
	MetricsAddr string `env:"METRICS_ADDR" envDefault:":8080"`
}
