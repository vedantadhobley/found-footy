// provider_integrity.go owns the worker-wide provider policy rollout switch.
package config

// ProviderIntegrityConfig is shared by active, staging, and ingestion writers.
type ProviderIntegrityConfig struct {
	Mode string `env:"PROVIDER_INTEGRITY_MODE" envDefault:"shadow"`
}
