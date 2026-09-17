// provider_fixture_health.go maps typed progress cursors and bounded incident
// evidence to SQL without touching the public fixture's recency or lifecycle.
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/vedantadhobley/found-footy/internal/domain/providerintegrity"
)

// loadFixtureHealth runs under the provider ordering lock. Missing rows start
// unknown; schema/evidence decode failures must not reset existing history.
func loadFixtureHealth(ctx context.Context, tx pgx.Tx, id int64) (providerintegrity.FixtureHealth, error) {
	s := providerintegrity.FixtureHealth{FixtureID: id}
	var lastUsable, continuous, progress, phase *time.Time
	var data []byte
	err := tx.QueryRow(ctx, `SELECT last_poll_id,last_observed_at,last_usable_at,continuous_since,clock_progress_at,
		phase_since,observed_status,clock_status,clock_elapsed,clock_extra,evidence FROM provider_fixture_health WHERE fixture_id=$1`, id).
		Scan(&s.LastPollID, &s.LastObservedAt, &lastUsable, &continuous, &progress, &phase, &s.ObservedStatus, &s.ClockStatus, &s.ClockElapsed, &s.ClockExtra, &data)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	s.LastObservedAt = s.LastObservedAt.UTC()
	if lastUsable != nil {
		s.LastUsableAt = lastUsable.UTC()
	}
	if continuous != nil {
		s.ContinuousSince = continuous.UTC()
	}
	if progress != nil {
		s.ClockProgressAt = progress.UTC()
	}
	if phase != nil {
		s.PhaseSince = phase.UTC()
	}
	if err := json.Unmarshal(data, &s.Evidence); err != nil {
		return s, err
	}
	if s.Evidence.Version != providerintegrity.HealthEvidenceVersion {
		return s, fmt.Errorf("unsupported fixture health evidence version")
	}
	return s, nil
}

// storeFixtureHealth persists SQL-queryable progress and the unresolved causes.
func storeFixtureHealth(ctx context.Context, tx pgx.Tx, s providerintegrity.FixtureHealth) error {
	data, err := json.Marshal(s.Evidence)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO provider_fixture_health
		(fixture_id,last_poll_id,last_observed_at,last_usable_at,continuous_since,clock_progress_at,
		phase_since,observed_status,clock_status,clock_elapsed,clock_extra,evidence)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (fixture_id) DO UPDATE SET last_poll_id=EXCLUDED.last_poll_id,last_observed_at=EXCLUDED.last_observed_at,
		last_usable_at=EXCLUDED.last_usable_at,continuous_since=EXCLUDED.continuous_since,clock_progress_at=EXCLUDED.clock_progress_at,
		phase_since=EXCLUDED.phase_since,observed_status=EXCLUDED.observed_status,clock_status=EXCLUDED.clock_status,clock_elapsed=EXCLUDED.clock_elapsed,
		clock_extra=EXCLUDED.clock_extra,evidence=EXCLUDED.evidence`, s.FixtureID, s.LastPollID, s.LastObservedAt,
		healthTime(s.LastUsableAt), healthTime(s.ContinuousSince), healthTime(s.ClockProgressAt), healthTime(s.PhaseSince),
		s.ObservedStatus, s.ClockStatus, s.ClockElapsed, s.ClockExtra, data)
	return err
}

// healthTime writes missing evidence as NULL rather than year-one timestamps.
func healthTime(at time.Time) *time.Time {
	if at.IsZero() {
		return nil
	}
	return &at
}
