// Package event owns the durable Postgres event-log composer and the separate
// NATS live-feed publisher. Callers choose the required delivery boundary;
// neither adapter hides a dual write.
package event
