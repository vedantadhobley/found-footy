// EventConfig — env-driven settings for the found-footy eventing layer
// (the NATS live-feed producer). Currently just the envelope source
// identity; grows if the eventing layer gains more tunables.
package config

// EventConfig configures the eventing/producer layer. Source is stamped
// into every NATS envelope's `source` field so consumers on a shared or
// bridged bus can tell found-footy-dev's messages from found-footy-prod's
// (the subject namespaces the project; source carries the environment).
type EventConfig struct {
	// Source is the producer identity including environment —
	// "found-footy-dev" or "found-footy-prod". Defaults to the dev
	// identity; the prod compose MUST override it to "found-footy-prod".
	Source string `env:"EVENT_SOURCE" envDefault:"found-footy-dev"`
}
