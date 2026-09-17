// provider_health_repo.go commits provider evidence and logical poll receipts
// together. This shadow store never writes canonical fixtures, events, or outbox.
package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/vedantadhobley/found-footy/internal/domain/providerintegrity"
)

// ProviderHealthRepo serializes the short evidence transaction, not provider
// requests or per-fixture reconciliation. Each deployment has its own database.
type ProviderHealthRepo struct{ pool *Pool }

// NewProviderHealthRepo shares the existing traced and metered Postgres pool.
func NewProviderHealthRepo(pool *Pool) *ProviderHealthRepo { return &ProviderHealthRepo{pool: pool} }

// RecordFetchFailure retains each failed attempt under its logical poll. A
// repeated write of the same attempt is a no-op; final receipts are immutable.
func (r *ProviderHealthRepo) RecordFetchFailure(ctx context.Context, failure providerintegrity.FetchFailureEvidence) error {
	poll := providerintegrity.HealthPoll{ID: failure.PollID, At: failure.ObservedAt, Observations: failure.Failures}
	if err := poll.Validate(); err != nil {
		return err
	}
	if failure.Attempt <= 0 || failure.Attempt > 10 {
		return fmt.Errorf("invalid provider fetch attempt")
	}
	for _, observation := range failure.Failures {
		if observation.Availability == providerintegrity.AvailabilityUsable {
			return fmt.Errorf("failure receipt contains usable data")
		}
	}
	data, err := json.Marshal(map[string][]providerintegrity.HealthObservation{strconv.Itoa(failure.Attempt): failure.Failures})
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `INSERT INTO provider_health_polls (poll_id, observed_at, failed_attempts)
		VALUES ($1,$2,$3) ON CONFLICT (poll_id) DO UPDATE
		SET failed_attempts = provider_health_polls.failed_attempts || EXCLUDED.failed_attempts
		WHERE provider_health_polls.result IS NULL AND provider_health_polls.observed_at = EXCLUDED.observed_at
		AND NOT (provider_health_polls.failed_attempts ? $4)`, failure.PollID, failure.ObservedAt, data, strconv.Itoa(failure.Attempt))
	return err
}

// Observe records exactly one finalized assessment per logical poll. Delayed
// cycles cannot replace current evidence; retries return the committed result.
func (r *ProviderHealthRepo) Observe(ctx context.Context, poll providerintegrity.HealthPoll) (providerintegrity.HealthResult, error) {
	var out providerintegrity.HealthResult
	poll.At = poll.At.UTC().Truncate(time.Microsecond)
	if err := poll.Validate(); err != nil {
		return out, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	state, err := lockProviderHealth(ctx, tx)
	if err != nil {
		return out, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO provider_health_polls (poll_id,observed_at) VALUES ($1,$2) ON CONFLICT DO NOTHING`, poll.ID, poll.At)
	if err != nil {
		return out, err
	}
	var receiptAt time.Time
	var failuresJSON, resultJSON []byte
	if err := tx.QueryRow(ctx, `SELECT observed_at, failed_attempts, result FROM provider_health_polls WHERE poll_id=$1 FOR UPDATE`, poll.ID).
		Scan(&receiptAt, &failuresJSON, &resultJSON); err != nil {
		return out, err
	}
	if !receiptAt.Equal(poll.At.Truncate(time.Microsecond)) {
		return out, fmt.Errorf("provider health poll ID reused with another time")
	}
	if resultJSON != nil {
		if err := json.Unmarshal(resultJSON, &out); err != nil {
			return out, err
		}
		return out, nil
	}
	if !poll.At.After(state.LastObservedAt) {
		out = providerintegrity.HealthResult{ObservedAt: poll.At, Ignored: true, Generation: state.Generation, ProviderPolicy: state.Policy}
	} else {
		if err := verifyHealthBaselines(ctx, tx, poll); err != nil {
			return out, err
		}
		var attempts map[string][]providerintegrity.HealthObservation
		if err := json.Unmarshal(failuresJSON, &attempts); err != nil {
			return out, err
		}
		interrupted := make(map[int64]bool)
		for _, failures := range attempts {
			for _, failure := range failures {
				interrupted[failure.FixtureID] = true
			}
		}
		for i := range poll.Observations {
			poll.Observations[i].Interrupted = poll.Observations[i].Interrupted || interrupted[poll.Observations[i].FixtureID]
		}
		states := make([]providerintegrity.FixtureHealth, 0, len(poll.Observations))
		var transitions []providerintegrity.HealthTransition
		for _, observation := range poll.Observations {
			fixtureState, err := loadFixtureHealth(ctx, tx, observation.FixtureID)
			if err != nil {
				return out, err
			}
			transitions = append(transitions, providerintegrity.ObserveFixture(&fixtureState, poll, observation)...)
			if err := storeFixtureHealth(ctx, tx, fixtureState); err != nil {
				return out, err
			}
			states = append(states, fixtureState)
		}
		out = providerintegrity.ObserveProvider(&state, poll, states, transitions)
		out.FailedAttempts = len(attempts)
		if err := storeProviderHealth(ctx, tx, state); err != nil {
			return out, err
		}
	}
	data, err := json.Marshal(out)
	if err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, `UPDATE provider_health_polls SET result=$2 WHERE poll_id=$1`, poll.ID, data); err != nil {
		return out, err
	}
	// Small indexed batches bound maintenance work after extended downtime.
	// Unresolved incidents live in state rows and are never expired here.
	if _, err := tx.Exec(ctx, `DELETE FROM provider_health_polls WHERE poll_id IN (
		SELECT poll_id FROM provider_health_polls WHERE observed_at < $1 ORDER BY observed_at LIMIT 256
	)`, state.LastObservedAt.Add(-providerintegrity.HealthReceiptRetention)); err != nil {
		return out, err
	}
	if err := tx.Commit(ctx); err != nil {
		return out, err
	}
	return out, nil
}

// verifyHealthBaselines closes the collector's read/commit race. Every
// provider-derived writer takes the provider policy lock, so last_polled_at
// cannot change after this check until the evidence transaction commits.
func verifyHealthBaselines(ctx context.Context, tx pgx.Tx, poll providerintegrity.HealthPoll) error {
	for _, observation := range poll.Observations {
		if observation.Availability != providerintegrity.AvailabilityUsable {
			continue
		}
		var stored *time.Time
		if err := tx.QueryRow(ctx, `SELECT last_polled_at FROM fixtures WHERE id=$1`, observation.FixtureID).Scan(&stored); err != nil {
			return fmt.Errorf("verify provider health fixture %d baseline: %w", observation.FixtureID, err)
		}
		if !sameOptionalTime(stored, observation.BaselineLastPolled) {
			return fmt.Errorf("verify provider health fixture %d baseline: %w", observation.FixtureID, providerintegrity.ErrStaleMutationPlan)
		}
	}
	return nil
}

// lockProviderHealth is the common ordering lock for evidence-only commits.
func lockProviderHealth(ctx context.Context, tx pgx.Tx) (providerintegrity.ProviderHealth, error) {
	var s providerintegrity.ProviderHealth
	var at *time.Time
	var data []byte
	err := tx.QueryRow(ctx, `SELECT last_observed_at,generation,policy,evidence FROM provider_health WHERE id=1 FOR UPDATE`).Scan(&at, &s.Generation, &s.Policy, &data)
	if err != nil {
		return s, err
	}
	if at != nil {
		s.LastObservedAt = at.UTC()
	}
	if err := json.Unmarshal(data, &s.Evidence); err != nil {
		return s, err
	}
	if s.Evidence.Version != providerintegrity.HealthEvidenceVersion {
		return s, fmt.Errorf("unsupported provider health evidence version")
	}
	return s, nil
}

// storeProviderHealth persists generation and recovery evidence in one row.
func storeProviderHealth(ctx context.Context, tx pgx.Tx, s providerintegrity.ProviderHealth) error {
	data, err := json.Marshal(s.Evidence)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE provider_health SET last_observed_at=$1,generation=$2,policy=$3,evidence=$4 WHERE id=1`, s.LastObservedAt, s.Generation, s.Policy, data)
	return err
}
