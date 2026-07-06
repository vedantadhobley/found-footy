// Package nats wraps the workspace NATS client: pub/sub + JetStream.
// Backs the semantic event bus (dual-write with event_log per §9 event
// composer). Variadic Subscribe merges subject patterns.
package nats
