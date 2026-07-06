// Package event is the dual-write composer: INSERT INTO event_log
// (durable, via pg) then nats.Publish (best-effort realtime fan-out).
// The event_log INSERT populates ev.EventLogID via RETURNING id so
// the NATS payload carries a valid Last-Event-Id cursor for SSE
// reconnect. See §9 event composer.
package event
