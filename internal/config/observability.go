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
}
