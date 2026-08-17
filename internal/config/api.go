// api.go — APIConfig for the public read-only HTTP surface.
// Separate from the :8080 bootstrap metrics/healthz — the api binary serves
// both. Bound to a container port only; Caddy fronts it per the workspace
// host-port rule.
package config

import "time"

// APIConfig configures the internal/api HTTP surface (cmd/api).
type APIConfig struct {
	// ListenAddr is the public API bind address. Distinct from the bootstrap
	// metrics/healthz :8080.
	ListenAddr string `env:"API_LISTEN_ADDR" envDefault:":8081"`

	// ReadTimeout / WriteTimeout bound a single request so a slow client
	// can't hold a connection open indefinitely.
	ReadTimeout  time.Duration `env:"API_READ_TIMEOUT" envDefault:"15s"`
	WriteTimeout time.Duration `env:"API_WRITE_TIMEOUT" envDefault:"30s"`

	// ShutdownTimeout bounds graceful drain on SIGTERM.
	ShutdownTimeout time.Duration `env:"API_SHUTDOWN_TIMEOUT" envDefault:"10s"`
}
