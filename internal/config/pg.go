package config

import "time"

// PGConfig covers the Postgres substrate every binary that touches the
// database uses. Single URL is authoritative — split-field form gets
// added if a secret manager forces it, but URL keeps local dev + tests
// simple.
//
// URL is not tagged required at the env layer because Phase S1 binaries
// don't need Postgres yet; the pg package's constructor returns a
// descriptive error when a binary that DOES need it starts without the
// env var set.
type PGConfig struct {
	// URL is a libpq / pgx connection string. Example:
	//   postgres://ff:ff@postgres:5432/found_footy?sslmode=disable
	URL string `env:"POSTGRES_URL"`

	// MaxConns caps the pool per binary. 10 × 8 worker replicas + api +
	// scaler stays under Postgres's default max_connections=100. Retune
	// after Phase O has real load data.
	MaxConns int32 `env:"POSTGRES_MAX_CONNS" envDefault:"10"`

	// MinConns keeps a warm connection floor to absorb burst without
	// paying handshake cost every time.
	MinConns int32 `env:"POSTGRES_MIN_CONNS" envDefault:"2"`

	// ConnectTimeout bounds the initial connect handshake. Independent of
	// per-query context timeouts.
	ConnectTimeout time.Duration `env:"POSTGRES_CONNECT_TIMEOUT" envDefault:"10s"`
}
