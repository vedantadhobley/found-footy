// NATSConfig — env-driven settings for the workspace NATS bus adapter.
package config

import "time"

// NATSConfig covers the workspace bus used for the environment-scoped live
// feed. Video bytes never traverse it.
//
// URL is not tagged required at the env layer; the adapter constructor returns
// a descriptive error when a consuming binary starts without it.
type NATSConfig struct {
	// URL is the NATS server address. Cluster form is comma-separated:
	//   nats://a:4222,nats://b:4222,nats://c:4222
	URL string `env:"NATS_URL"`

	// Account is the NATS account this binary authenticates against —
	// each project on the workspace NATS gets its own isolated account
	// so subjects like event.detected don't collide across projects.
	Account string `env:"NATS_ACCOUNT"`

	// CredsFile is a path to the NATS credentials (.creds) file that
	// authenticates against the account. Empty = no auth (dev/test).
	CredsFile string `env:"NATS_CREDS_FILE"`

	// ClientName shows up in NATS server logs and monitoring — one per
	// binary/replica so operators can distinguish "which worker replica
	// stopped publishing" from a common connection name.
	ClientName string `env:"NATS_CLIENT_NAME"`

	// ConnectTimeout bounds the initial connect handshake. Independent
	// of per-publish or per-request timeouts.
	ConnectTimeout time.Duration `env:"NATS_CONNECT_TIMEOUT" envDefault:"10s"`

	// ReconnectWait is the delay between reconnection attempts when the
	// server drops. Default matches nats.go's default (2s).
	ReconnectWait time.Duration `env:"NATS_RECONNECT_WAIT" envDefault:"2s"`

	// MaxReconnects caps how many times the client retries before
	// giving up. -1 = infinite (default; matches nats.go default of -1
	// after the workspace's always-on NATS model).
	MaxReconnects int `env:"NATS_MAX_RECONNECTS" envDefault:"-1"`
}
