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

	// LokiEnabled is a hint the logging layer can use to skip building
	// certain expensive fields when nothing downstream will consume them.
	// Defaults true because prod always has Loki; dev can override to
	// false for standalone runs.
	LokiEnabled bool `env:"LOKI_ENABLED" envDefault:"true"`

	// MetricsAddr is the listen address for the Prometheus /metrics
	// endpoint that every binary exposes. Defaults to :8080 — each
	// docker-compose service can override via env if the port collides
	// with the binary's application HTTP surface (e.g. the api binary
	// serves the public API on the same port later; the split gets
	// resolved in Phase A).
	MetricsAddr string `env:"METRICS_ADDR" envDefault:":8080"`
}
