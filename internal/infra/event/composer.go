// Composer dual-writes semantic events to (pg event_log, NATS). pg
// is the source of truth (durable, gap-detectable via outbox_cursor);
// NATS is best-effort real-time fan-out. See decisions.md 2026-07-16
// "Downstream workflow spawn via Temporal-direct + register-on-flip"
// for the rationale on NATS staying scoped to external fan-out (SSE,
// webhooks) — Composer is NOT the trigger path for named internal
// workflows.
package event

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/infra/nats"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// Composer bundles a pg.Pool + nats.Conn + Instruments and exposes a
// single Publish method. Concurrent-safe: pgxpool + nats.Conn are
// both safe for concurrent use by multiple goroutines.
type Composer struct {
	pg   *pg.Pool
	nats *nats.Conn
	ins  *Instruments
}

// New constructs a Composer. All three arguments are required.
// Returns an error rather than panicking so callers can wrap the
// construction failure into their bootstrap sequence cleanly.
func New(pool *pg.Pool, conn *nats.Conn, ins *Instruments) (*Composer, error) {
	if pool == nil {
		return nil, fmt.Errorf("event.New: pg.Pool is required")
	}
	if conn == nil {
		return nil, fmt.Errorf("event.New: nats.Conn is required")
	}
	if ins == nil {
		return nil, fmt.Errorf("event.New: Instruments is required (call RegisterMetrics first)")
	}
	return &Composer{pg: pool, nats: conn, ins: ins}, nil
}

// Publish writes payload to event_log and publishes an envelope to
// NATS on the subject named by kind. Returns the event_log.id (usable
// as a Last-Event-Id cursor for SSE reconnect) and an error.
//
// Semantics:
//   - Return non-nil error → pg INSERT failed. Nothing was published.
//     Caller should treat this as a hard failure (retry-able if the
//     activity is idempotent, or propagate).
//   - Return nil error → pg INSERT succeeded. NATS publish MAY have
//     failed (skew metric increments, WARN log emitted) but the
//     truth is in event_log and the outbox catch-up worker (future)
//     will republish gaps on its next tick. Caller can proceed as if
//     both writes happened.
//
// eventID / fixtureID may be uuid.Nil / 0 for kinds that don't
// reference a specific one — they'll be stored as SQL NULL and
// omitted from the NATS envelope.
func (c *Composer) Publish(
	ctx context.Context,
	kind Kind,
	eventID uuid.UUID,
	fixtureID int64,
	payload any,
) (int64, error) {
	if !kind.Valid() {
		return 0, fmt.Errorf("event.Publish: unknown kind %q", kind)
	}

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("event.Publish: marshal payload for kind %s: %w", kind, err)
	}

	start := time.Now()

	// pg column layout: (event_type, event_id, fixture_id, video_share_id,
	// payload). video_share_id stays NULL for O3/a's six kinds — later
	// phases (ranking, upload) will populate it.
	eventIDPtr := nullableUUID(eventID)
	fixtureIDPtr := nullableInt64(fixtureID)

	var logID int64
	row := c.pg.QueryRow(ctx, `
		INSERT INTO event_log (event_type, event_id, fixture_id, payload)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, string(kind), eventIDPtr, fixtureIDPtr, payloadJSON)
	if err := row.Scan(&logID); err != nil {
		c.ins.publishes.WithLabelValues(string(kind), "pg_write_failure").Inc()
		c.ins.emitEvent(ctx, logging.LevelError, vocabulary.ActionEventPublishFailed,
			"event composer pg write failed",
			logging.String("kind", string(kind)),
			logging.Err(err),
		)
		return 0, fmt.Errorf("event.Publish: pg insert for kind %s: %w", kind, err)
	}

	// Envelope wraps the payload with the event_log_id (SSE cursor) +
	// timestamp + kind so consumers don't need a second lookup for
	// the routing metadata. json.RawMessage avoids re-encoding.
	envelope := struct {
		EventLogID int64           `json:"event_log_id"`
		Kind       string          `json:"kind"`
		OccurredAt time.Time       `json:"occurred_at"`
		EventID    *uuid.UUID      `json:"event_id,omitempty"`
		FixtureID  *int64          `json:"fixture_id,omitempty"`
		Payload    json.RawMessage `json:"payload"`
	}{
		EventLogID: logID,
		Kind:       string(kind),
		OccurredAt: start,
		EventID:    eventIDPtr,
		FixtureID:  fixtureIDPtr,
		Payload:    payloadJSON,
	}

	envelopeJSON, err := json.Marshal(envelope)
	if err != nil {
		// Extraordinarily rare — the envelope is well-typed. Treat
		// as skew: pg row exists, no NATS message shipped, outbox
		// worker will republish.
		c.ins.skew.Inc()
		c.ins.publishes.WithLabelValues(string(kind), "envelope_marshal_failure").Inc()
		c.ins.emitEvent(ctx, logging.LevelError, vocabulary.ActionEventPublishSkew,
			"event composer envelope marshal failed after pg write",
			logging.String("kind", string(kind)),
			logging.Int64("event_log_id", logID),
			logging.Err(err),
		)
		return logID, nil
	}

	if err := c.nats.Publish(string(kind), envelopeJSON); err != nil {
		c.ins.skew.Inc()
		c.ins.publishes.WithLabelValues(string(kind), "nats_publish_failure").Inc()
		c.ins.emitEvent(ctx, logging.LevelWarn, vocabulary.ActionEventPublishSkew,
			"event composer NATS publish failed after pg write",
			logging.String("kind", string(kind)),
			logging.Int64("event_log_id", logID),
			logging.Err(err),
		)
		return logID, nil
	}

	elapsed := time.Since(start)
	c.ins.publishDuration.WithLabelValues(string(kind)).Observe(elapsed.Seconds())
	c.ins.publishes.WithLabelValues(string(kind), "success").Inc()
	c.ins.emitEvent(ctx, logging.LevelDebug, vocabulary.ActionEventPublish,
		"event composer publish ok",
		logging.String("kind", string(kind)),
		logging.Int64("event_log_id", logID),
		logging.Int64("elapsed_us", elapsed.Microseconds()),
	)
	return logID, nil
}

// nullableUUID returns a pointer for pgx if u is non-zero; nil
// otherwise. Sending nil for a UUID column stores SQL NULL.
func nullableUUID(u uuid.UUID) *uuid.UUID {
	if u == uuid.Nil {
		return nil
	}
	return &u
}

// nullableInt64 returns a pointer for pgx if i is non-zero; nil
// otherwise. fixture_id = 0 is not a real fixture in our data.
func nullableInt64(i int64) *int64 {
	if i == 0 {
		return nil
	}
	return &i
}
