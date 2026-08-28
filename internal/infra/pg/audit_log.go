// Transactional persistence for required semantic-transition audit records.
package pg

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/vedantadhobley/found-footy/internal/contract/auditlog"
)

// insertAuditLog appends record through tx. It deliberately accepts only a
// transaction so callers cannot reintroduce a split state/audit write.
func insertAuditLog(ctx context.Context, tx pgx.Tx, record auditlog.Record) error {
	if !record.Valid() {
		return fmt.Errorf("pg.insertAuditLog: invalid audit record")
	}
	var eventID any
	if record.EventID() != uuid.Nil {
		eventID = record.EventID()
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO event_log (event_type, event_id, fixture_id, payload)
		VALUES ($1, $2, $3, $4)
	`, record.Kind().String(), eventID, record.FixtureID(), record.Payload()); err != nil {
		return fmt.Errorf("pg.insertAuditLog: insert %s: %w", record.Kind(), err)
	}
	return nil
}
