// Consistent selection snapshots retain exact source identity and each variant's own acceptance.
package pg

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/vedantadhobley/found-footy/internal/domain/video"
	dvision "github.com/vedantadhobley/found-footy/internal/domain/vision"
)

// LoadSelection reads a repeatable snapshot without taking the production write path.
func (r *PlacementRepo) LoadSelection(ctx context.Context, eventID uuid.UUID) (video.SelectionSnapshot, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return video.SelectionSnapshot{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	snapshot, err := loadSelectionTx(ctx, tx, eventID, false)
	if err != nil {
		return snapshot, err
	}
	return snapshot, tx.Commit(ctx)
}

// loadSelectionTx also supports the event-locked write transaction. Row locks
// cover old asset/share writers which predate the common event lock.
func loadSelectionTx(ctx context.Context, tx pgx.Tx, eventID uuid.UUID, lock bool) (video.SelectionSnapshot, error) {
	s := video.SelectionSnapshot{EventID: eventID}
	if err := tx.QueryRow(ctx, `SELECT fixture_id,removed FROM events WHERE id=$1`, eventID).Scan(&s.FixtureID, &s.Removed); err != nil {
		return s, err
	}
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	rows, err := tx.Query(ctx, "SELECT "+assetColumns+" FROM video_assets WHERE event_id=$1 ORDER BY id"+suffix, eventID)
	if err != nil {
		return s, err
	}
	indexes := make(map[uuid.UUID]int)
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			rows.Close()
			return s, err
		}
		indexes[a.ID] = len(s.Nodes)
		s.Nodes = append(s.Nodes, video.SelectionNode{Asset: a})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return s, err
	}
	rows, err = tx.Query(ctx, "SELECT "+shareColumns+" FROM video_shares WHERE event_id=$1 ORDER BY id"+suffix, eventID)
	if err != nil {
		return s, err
	}
	for rows.Next() {
		share, err := scanShare(rows)
		if err != nil {
			rows.Close()
			return s, err
		}
		i, exists := indexes[share.AssetID]
		if !exists || s.Nodes[i].Share != nil {
			rows.Close()
			return s, fmt.Errorf("selection share has missing or repeated asset")
		}
		s.Nodes[i].Share = share
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return s, err
	}
	// A share keeps its original public metadata. For a never-public variant,
	// the earliest retained acceptance is used, never the most favorable verdict.
	// video_asset_validations_event keeps this lookup local to the event even
	// as validation history from expired fixtures accumulates.
	rows, err = tx.Query(ctx, `SELECT DISTINCT ON (asset_id) asset_id,evidence
		FROM video_asset_validations WHERE event_id=$1 ORDER BY asset_id,recorded_at,id`, eventID)
	if err != nil {
		return s, err
	}
	for rows.Next() {
		var id uuid.UUID
		var body []byte
		if err := rows.Scan(&id, &body); err != nil {
			rows.Close()
			return s, err
		}
		var proof dvision.Evidence
		if err := json.Unmarshal(body, &proof); err != nil {
			rows.Close()
			return s, err
		}
		i, exists := indexes[id]
		if !exists {
			rows.Close()
			return s, fmt.Errorf("validation asset missing from snapshot")
		}
		s.Nodes[i].Validation = &proof
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return s, err
	}
	rows, err = tx.Query(ctx, `SELECT id,observed_asset_id,credited_asset_id FROM event_search_candidates
		WHERE event_id=$1 AND outcome_class IN ('promoted','duplicate','superseded') ORDER BY id`+suffix, eventID)
	if err != nil {
		return s, err
	}
	for rows.Next() {
		var source video.SelectionSource
		var observed, credited *uuid.UUID
		if err := rows.Scan(&source.ID, &observed, &credited); err != nil {
			rows.Close()
			return s, err
		}
		if observed != nil {
			source.ObservedAssetID = *observed
		}
		if credited != nil {
			source.CreditedAssetID = *credited
		}
		s.Sources = append(s.Sources, source)
	}
	rows.Close()
	return s, rows.Err()
}
