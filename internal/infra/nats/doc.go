// Package nats wraps the workspace NATS client: pub/sub + JetStream.
// It backs live fan-out independently from the Postgres event_log audit plane.
// Variadic Subscribe merges subject patterns.
package nats
