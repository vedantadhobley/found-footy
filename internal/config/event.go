// EventConfig — env-driven settings for the found-footy eventing layer (the
// NATS live-feed producer). Currently just the deployment environment; grows
// if the eventing layer gains more tunables.
package config

// EventConfig configures the eventing/producer layer.
type EventConfig struct {
	// Environment is the deployment env ("dev" / "prod"). One knob, two derived
	// outputs: the NATS subject token — found-footy.<env>.<topic>, so a consumer
	// filters by environment at subscription (a prod bridge takes
	// found-footy.prod.> and never sees dev) — AND the envelope `source` stamp
	// (found-footy-<env>). Defaults to dev; the prod compose MUST set
	// EVENT_ENV=prod. decisions.md 2026-08-15.
	Environment string `env:"EVENT_ENV" envDefault:"dev"`
}
