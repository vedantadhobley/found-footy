// Composer appends semantic events to the pg event_log — the durable audit
// plane. Per decisions.md 2026-08-14 the NATS half moved out to
// event.NatsPublisher (the live-fanout plane), so the Composer is now
// event_log-only: every transition still lands a per-transition row (the fine
// grain + forensic substrate), but nothing goes on the bus from here.
package event

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/infra/pg"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// Composer bundles a pg.Pool + Instruments and exposes Publish, which appends
// one row to event_log. Concurrent-safe: pgxpool is safe for concurrent use by
// multiple goroutines.
type Composer struct {
	pg  *pg.Pool
	ins *Instruments
}

// New constructs a Composer. Both arguments are required. Returns an error
// rather than panicking so callers wrap the failure into their bootstrap
// sequence cleanly.
func New(pool *pg.Pool, ins *Instruments) (*Composer, error) {
	if pool == nil {
		return nil, fmt.Errorf("event.New: pg.Pool is required")
	}
	if ins == nil {
		return nil, fmt.Errorf("event.New: Instruments is required (call RegisterMetrics first)")
	}
	return &Composer{pg: pool, ins: ins}, nil
}

// Publish appends payload to event_log under the semantic type named by kind
// and returns the event_log.id. The live-fanout plane (NATS) is a separate path
// (event.NatsPublisher) — this method touches only pg.
//
// A non-nil error means the INSERT failed (nothing was written); the caller may
// retry if idempotent, or propagate. eventID / fixtureID may be uuid.Nil / 0
// for kinds that don't reference a specific one — they store as SQL NULL.
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

	elapsed := time.Since(start)
	c.ins.publishDuration.WithLabelValues(string(kind)).Observe(elapsed.Seconds())
	c.ins.publishes.WithLabelValues(string(kind), "success").Inc()
	c.ins.emitEvent(ctx, logging.LevelDebug, vocabulary.ActionEventPublish,
		"event composer event_log write ok",
		logging.String("kind", string(kind)),
		logging.Int64("event_log_id", logID),
		logging.Int64("elapsed_us", elapsed.Microseconds()),
	)
	return logID, nil
}

// nullableUUID returns a pointer for pgx if u is non-zero; nil otherwise.
// Sending nil for a UUID column stores SQL NULL.
func nullableUUID(u uuid.UUID) *uuid.UUID {
	if u == uuid.Nil {
		return nil
	}
	return &u
}

// nullableInt64 returns a pointer for pgx if i is non-zero; nil otherwise.
// fixture_id = 0 is not a real fixture in our data.
func nullableInt64(i int64) *int64 {
	if i == 0 {
		return nil
	}
	return &i
}
