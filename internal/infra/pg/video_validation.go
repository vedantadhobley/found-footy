// Accepted validation evidence commits once with its exact asset, never with the selected replacement.
package pg

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/vedantadhobley/found-footy/internal/domain/video"
)

// persistPlacementValidation is an immutable, retry-safe insert under the
// placement's event lock. A conflicting evaluation ID aborts the whole placement.
func persistPlacementValidation(ctx context.Context, tx pgx.Tx, in video.ClipPlacement) error {
	if in.Validation == nil {
		return nil // old histories and exact recurrences did not run a new evaluation
	}
	observed, err := getAssetTx(ctx, tx, in.ObservedAssetID, false)
	if err != nil {
		return fmt.Errorf("read validation asset: %w", err)
	}
	if in.Validation.MD5 != hex.EncodeToString(observed.MD5) {
		return fmt.Errorf("validation does not describe observed bytes")
	}
	evidence, err := json.Marshal(in.Validation)
	if err != nil {
		return fmt.Errorf("encode validation: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO video_asset_validations (id, asset_id, event_id, fixture_id, evidence)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (id) DO NOTHING
	`, in.Validation.ID, in.ObservedAssetID, in.EventID, in.FixtureID, evidence); err != nil {
		return fmt.Errorf("insert validation: %w", err)
	}
	var same bool
	if err := tx.QueryRow(ctx, `
		SELECT asset_id = $2 AND event_id = $3 AND fixture_id = $4 AND evidence = $5::jsonb
		FROM video_asset_validations WHERE id = $1
	`, in.Validation.ID, in.ObservedAssetID, in.EventID, in.FixtureID, evidence).Scan(&same); err != nil {
		return fmt.Errorf("read validation retry: %w", err)
	}
	if !same {
		return fmt.Errorf("validation identity reused with different evidence")
	}
	return nil
}
