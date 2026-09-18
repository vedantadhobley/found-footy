// provider_health_repo_test.go uses disposable PostgreSQL to prove durable
// cursors, atomic receipts, retry isolation, and zero canonical side effects.
package pg_test

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/vedantadhobley/found-footy/internal/domain/providerintegrity"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
	"github.com/vedantadhobley/found-footy/migrations"
)

// healthObservation models a valid stable match without live provider access.
func healthObservation(id int64, minute int) providerintegrity.HealthObservation {
	facts := providerintegrity.FixtureFacts{FixtureID: id, HomeID: 40, AwayID: 42, LeagueID: 39, HomeName: "Home", AwayName: "Away", LeagueName: "League",
		Status: "2h", Elapsed: &minute}
	return providerintegrity.HealthObservation{FixtureID: id, Availability: providerintegrity.AvailabilityUsable,
		Comparison: providerintegrity.FixtureComparison{Stored: facts, Observed: facts}}
}

// healthPoll gives each database test a stable microsecond-aligned timeline.
func healthPoll(tick int, observations ...providerintegrity.HealthObservation) providerintegrity.HealthPoll {
	return providerintegrity.HealthPoll{ID: fmt.Sprintf("health-%d", tick), At: time.Date(2026, 9, 16, 21, 0, 0, 0, time.UTC).Add(time.Duration(tick) * 30 * time.Second), Observations: observations}
}

// TestProviderHealthDurableRestartRetryAndIsolation proves evidence survives
// repository reconstruction and does not mutate public state or the outbox.
func TestProviderHealthDurableRestartRetryAndIsolation(t *testing.T) {
	ctx, pool, fixtures := setupRepo(t)
	require.NoError(t, fixtures.Insert(ctx, makeStaging(9701, healthPoll(0).At)))
	var before, after []byte
	require.NoError(t, pool.QueryRow(ctx, `SELECT to_jsonb(f) FROM fixtures f WHERE id=9701`).Scan(&before))
	o := healthObservation(9701, 65)
	var out providerintegrity.HealthResult
	for tick := 0; tick <= 6; tick++ {
		// Reconstruct on every poll: no in-process state may provide progress.
		var err error
		out, err = pg.NewProviderHealthRepo(pool).Observe(ctx, healthPoll(tick, o))
		require.NoError(t, err)
	}
	require.Equal(t, []int64{9701}, out.StalledIDs)
	require.Len(t, out.Transitions, 1)
	require.Equal(t, providerintegrity.PolicyTrusted, out.ProviderPolicy)
	// Simulate loss of the activity's response after commit and concurrent retry.
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	results := make(chan providerintegrity.HealthResult, 8)
	for range 8 {
		wg.Go(func() {
			result, err := pg.NewProviderHealthRepo(pool).Observe(ctx, healthPoll(6, o))
			errs <- err
			results <- result
		})
	}
	wg.Wait()
	close(errs)
	close(results)
	for err := range errs {
		require.NoError(t, err)
	}
	for result := range results {
		require.Equal(t, out, result)
	}
	old, err := pg.NewProviderHealthRepo(pool).Observe(ctx, providerintegrity.HealthPoll{ID: "late", At: healthPoll(2).At, Observations: []providerintegrity.HealthObservation{o}})
	require.NoError(t, err)
	require.True(t, old.Ignored)
	var progress, observed time.Time
	require.NoError(t, pool.QueryRow(ctx, `SELECT clock_progress_at,last_observed_at FROM provider_fixture_health WHERE fixture_id=9701`).Scan(&progress, &observed))
	require.True(t, progress.Equal(healthPoll(0).At))
	require.True(t, observed.Equal(healthPoll(6).At))
	for tick := 7; tick <= 8; tick++ {
		_, err = pg.NewProviderHealthRepo(pool).Observe(ctx, healthPoll(tick, healthObservation(9701, 65+tick-6)))
		require.NoError(t, err)
	}
	var evidence providerintegrity.FixtureHealthEvidence
	var data []byte
	require.NoError(t, pool.QueryRow(ctx, `SELECT evidence FROM provider_fixture_health WHERE fixture_id=9701`).Scan(&data))
	require.NoError(t, json.Unmarshal(data, &evidence))
	require.Empty(t, evidence.Incidents)
	require.NoError(t, pool.QueryRow(ctx, `SELECT to_jsonb(f) FROM fixtures f WHERE id=9701`).Scan(&after))
	require.JSONEq(t, string(before), string(after), "health must not change public recency or fixture fields")
	var audits int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM event_log`).Scan(&audits))
	require.Zero(t, audits)
}

// TestProviderHealthFailedAttemptsAndTotalFailure retains a failure followed by
// success without treating retries as independent observations or recovery.
func TestProviderHealthFailedAttemptsAndTotalFailure(t *testing.T) {
	ctx, pool, fixtures := setupRepo(t)
	require.NoError(t, fixtures.Insert(ctx, makeStaging(9702, healthPoll(0).At)))
	repo := pg.NewProviderHealthRepo(pool)
	o := healthObservation(9702, 65)
	for tick := 0; tick < 6; tick++ {
		_, err := repo.Observe(ctx, healthPoll(tick, o))
		require.NoError(t, err)
	}
	failure := providerintegrity.HealthObservation{FixtureID: 9702, Availability: providerintegrity.AvailabilityContract, FailureReason: "missing_requested_fixture"}
	receipt := providerintegrity.FetchFailureEvidence{PollID: healthPoll(6).ID, ObservedAt: healthPoll(6).At, Attempt: 1, Failures: []providerintegrity.HealthObservation{failure}}
	require.NoError(t, repo.RecordFetchFailure(ctx, receipt))
	require.NoError(t, repo.RecordFetchFailure(ctx, receipt))
	out, err := repo.Observe(ctx, healthPoll(6, o))
	require.NoError(t, err)
	require.Equal(t, 1, out.FailedAttempts)
	require.Empty(t, out.StalledIDs)
	receipt.Attempt = 2
	require.NoError(t, repo.RecordFetchFailure(ctx, receipt))
	var attempts int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM provider_health_polls p, jsonb_object_keys(p.failed_attempts) WHERE poll_id=$1`, receipt.PollID).Scan(&attempts))
	require.Equal(t, 1, attempts, "finalized receipt is immutable")
	out, err = repo.Observe(ctx, healthPoll(7, failure))
	require.NoError(t, err)
	require.Equal(t, []int64{9702}, out.UnavailableIDs)
	require.Len(t, out.Transitions, 1)
	out, err = repo.Observe(ctx, healthPoll(8, o))
	require.NoError(t, err)
	require.Empty(t, out.UnavailableIDs)
	require.True(t, out.Transitions[0].Resolved)
}

// TestProviderHealthAtomicFailureAndRetention checks transaction rollback,
// finite poll history, and unexpired unresolved incident state independently.
func TestProviderHealthAtomicFailureAndRetention(t *testing.T) {
	ctx, pool, fixtures := setupRepo(t)
	require.NoError(t, fixtures.Insert(ctx, makeStaging(9703, healthPoll(0).At)))
	repo := pg.NewProviderHealthRepo(pool)
	_, err := repo.Observe(ctx, healthPoll(0, healthObservation(9703, 65), healthObservation(99999, 65)))
	require.Error(t, err, "second fixture foreign key must abort the whole batch")
	var count int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM provider_fixture_health`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM provider_health_polls`).Scan(&count))
	require.Zero(t, count)
	for tick := 0; tick <= 6; tick++ {
		_, err = repo.Observe(ctx, healthPoll(tick, healthObservation(9703, 65)))
		require.NoError(t, err)
	}
	later := healthPoll(7, healthObservation(9703, 65))
	later.At = later.At.Add(15 * 24 * time.Hour)
	out, err := repo.Observe(ctx, later)
	require.NoError(t, err)
	require.Equal(t, []int64{9703}, out.StalledIDs, "silence does not resolve an existing incident")
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM provider_health_polls`).Scan(&count))
	require.Equal(t, 1, count)
	// An ancient retry whose receipt was pruned still cannot advance evidence.
	out, err = repo.Observe(ctx, healthPoll(6, healthObservation(9703, 90)))
	require.NoError(t, err)
	require.True(t, out.Ignored)
	_, err = pool.Exec(ctx, `UPDATE provider_fixture_health SET clock_elapsed=-1 WHERE fixture_id=9703`)
	require.Error(t, err)
	_, err = pool.Exec(ctx, `UPDATE provider_fixture_health SET evidence='{"Version":2}' WHERE fixture_id=9703`)
	require.Error(t, err)
}

// TestProviderHealthRejectsStaleCanonicalBaseline proves health evidence cannot
// describe an older canonical snapshot than the one guarded writers will see.
func TestProviderHealthRejectsStaleCanonicalBaseline(t *testing.T) {
	ctx, pool, fixtures := setupRepo(t)
	base := healthPoll(0).At
	f := guardedActiveFixture(t, 9705, base, 60, 1, 0)
	require.NoError(t, fixtures.Insert(ctx, f))
	observation := healthObservation(f.ID, 60)
	observation.BaselineLastPolled = copyTestTime(f.LastPolledAt)
	poll := healthPoll(1, observation)
	newer := base.Add(15 * time.Second)
	_, err := pool.Exec(ctx, `UPDATE fixtures SET last_polled_at=$2 WHERE id=$1`, f.ID, newer)
	require.NoError(t, err)

	_, err = pg.NewProviderHealthRepo(pool).Observe(ctx, poll)
	require.ErrorIs(t, err, providerintegrity.ErrStaleMutationPlan)
	var receipts, fixtureHealth int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM provider_health_polls WHERE poll_id=$1`, poll.ID).Scan(&receipts))
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM provider_fixture_health WHERE fixture_id=$1`, f.ID).Scan(&fixtureHealth))
	require.Zero(t, receipts)
	require.Zero(t, fixtureHealth)
}

// TestProviderHealthMigrationUpgrade creates the new objects from a database
// whose ledger contains the previous release, preserving all existing fixtures.
func TestProviderHealthMigrationUpgrade(t *testing.T) {
	ctx, pool, fixtures := setupRepo(t)
	require.NoError(t, pool.Migrate(ctx, migrations.FS))
	require.NoError(t, fixtures.Insert(ctx, makeStaging(9704, healthPoll(0).At)))
	// Only this disposable test database is reduced to the predecessor schema.
	_, err := pool.Exec(ctx, `DROP TABLE provider_mutation_receipts,provider_fixture_health,provider_health_polls,provider_health;
		DELETE FROM schema_migrations WHERE version >= '20260916_01_add_provider_health'`)
	require.NoError(t, err)
	require.NoError(t, pool.Migrate(ctx, migrations.FS))
	require.NoError(t, pool.VerifyMigrations(ctx, migrations.FS))
	f, err := fixtures.Get(ctx, 9704)
	require.NoError(t, err)
	require.EqualValues(t, 9704, f.ID)
	_, err = pg.NewProviderHealthRepo(pool).Observe(ctx, healthPoll(0, healthObservation(9704, 65)))
	require.NoError(t, err)
}
