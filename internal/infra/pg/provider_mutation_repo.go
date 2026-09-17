// provider_mutation_repo.go is the single transactional write boundary for
// provider observations. It keeps policy checks and every permitted mutation
// under locks while external Temporal, NATS, and object-store effects wait.
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/vedantadhobley/found-footy/internal/contract/auditlog"
	"github.com/vedantadhobley/found-footy/internal/contract/fixturepresentation"
	"github.com/vedantadhobley/found-footy/internal/domain/event"
	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/domain/providerintegrity"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

// ProviderMutationRepo applies one fixture observation per short transaction.
// Provider FOR SHARE locks permit different fixtures to commit concurrently but
// exclude the collector's provider-generation update until each commit ends.
type ProviderMutationRepo struct {
	pool *Pool
	mode providerintegrity.EnforcementMode
}

// NewProviderMutationRepo shares the existing traced and metered pool.
func NewProviderMutationRepo(pool *Pool, mode providerintegrity.EnforcementMode) *ProviderMutationRepo {
	return &ProviderMutationRepo{pool: pool, mode: mode}
}

// ApplyActive commits the fixture snapshot, event reconciliation, transition
// audits, downstream checklist rows, and completion as one guarded unit.
func (r *ProviderMutationRepo) ApplyActive(ctx context.Context, plan providerintegrity.ActiveMutationPlan) (providerintegrity.MutationResult, error) {
	var out providerintegrity.MutationResult
	if !r.mode.Valid() {
		return out, fmt.Errorf("invalid provider enforcement mode %q", r.mode)
	}
	normalizeActiveMutationPlan(&plan)
	if err := validateActiveMutationPlan(plan); err != nil {
		return out, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return out, fmt.Errorf("pg.ProviderMutationRepo.ApplyActive: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	providerAt, generation, providerPolicy, err := lockProviderMutationPolicy(ctx, tx)
	if err != nil {
		return out, err
	}
	if err := lockFixtureMutation(ctx, tx, plan.Fixture.ID); err != nil {
		return out, err
	}
	if receipt, found, err := loadMutationReceipt(ctx, tx, plan.Guard, plan.Fixture.ID); err != nil {
		return out, err
	} else if found {
		return receipt, tx.Commit(ctx)
	}

	current, err := lockedFixture(ctx, tx, plan.Fixture.ID)
	if err != nil {
		return out, err
	}
	if !plan.BaselineExists || current.State != fixture.StateActive ||
		!sameOptionalTime(current.LastPolledAt, plan.BaselineLastPolled) ||
		!newerObservation(plan.Guard.ObservedAt, current.LastPolledAt) ||
		!providerAt.Equal(plan.Guard.ObservedAt) || generation != plan.Guard.ExpectedGeneration {
		return r.commitStaleReceipt(ctx, tx, plan.Guard, current.ID, current, generation)
	}
	health, err := loadFixtureHealth(ctx, tx, current.ID)
	if err != nil {
		return out, err
	}
	if plan.Guard.RequireHealthReceipt && (health.LastPollID != plan.Guard.ObservationID || !health.LastObservedAt.Equal(plan.Guard.ObservedAt)) {
		return r.commitStaleReceipt(ctx, tx, plan.Guard, current.ID, current, generation)
	}
	decision, policy := providerintegrity.DecideMutation(r.mode, providerPolicy, providerintegrity.FixtureHealthPolicy(health), plan.LocalPolicy)
	decision.Writer = providerintegrity.MutationModeActive
	decision.Restricted = restrictedActiveOperations(plan, decision.RecommendedPolicy)
	out.Decision = &decision
	out.Policy = policy
	if policy == providerintegrity.PolicyRejected {
		out.Fixture = current
		return r.commitMutationResult(ctx, tx, plan.Guard, current.ID, generation, out)
	}
	if err := validateActiveEventScope(ctx, tx, current.ID, plan, policy); err != nil {
		return out, err
	}

	target := cloneFixture(plan.Fixture)
	if policy == providerintegrity.PolicyPositiveOnly {
		target = mergePositiveFixture(current, target)
	}
	if err := updateActiveFixtureTx(ctx, tx, target); err != nil {
		return out, err
	}
	out.Applied = true
	out.FixtureChanged = fixturePubliclyChanged(current, target)

	for _, op := range plan.Inserts {
		if err := insertPlannedEvent(ctx, tx, plan.WorkflowID, op); err != nil {
			return out, err
		}
		out.InsertedEventIDs = append(out.InsertedEventIDs, op.Event.ID)
		if op.Event.Player.Known() {
			out.NewNamedEventIDs = append(out.NewNamedEventIDs, op.Event.ID)
		}
		out.EventChanged = true
	}
	for _, op := range plan.Presences {
		triggered, err := registerPresenceTx(ctx, tx, plan.WorkflowID, op)
		if err != nil {
			return out, err
		}
		if triggered {
			out.StableEventIDs = append(out.StableEventIDs, op.EventID)
			out.EventChanged = true
		}
	}
	for _, op := range plan.Updates {
		changed, err := updateEventFieldsTx(ctx, tx, op, policy)
		if err != nil {
			return out, err
		}
		out.EventChanged = out.EventChanged || changed
	}
	if policy == providerintegrity.PolicyTrusted {
		for _, id := range plan.UnknownDeletes {
			tag, err := tx.Exec(ctx, `DELETE FROM events WHERE id=$1 AND fixture_id=$2 AND debounce_count=0`, id, current.ID)
			if err != nil {
				return out, err
			}
			if tag.RowsAffected() == 1 {
				out.UnknownDropped++
				out.EventChanged = true
			}
		}
		for _, op := range plan.Absences {
			removed, err := registerAbsenceTx(ctx, tx, plan.WorkflowID, op)
			if err != nil {
				return out, err
			}
			if removed {
				out.RemovedEventIDs = append(out.RemovedEventIDs, op.EventID)
				out.EventChanged = true
			}
		}
		completed, err := completeFixtureIfReadyTx(ctx, tx, target, plan.Completion)
		if err != nil {
			return out, err
		}
		out.Completed = completed
		out.FixtureChanged = out.FixtureChanged || completed
	}
	out.Fixture, err = lockedFixture(ctx, tx, current.ID)
	if err != nil {
		return out, err
	}
	classifyMutationFixtureChanges(&out, current)
	return r.commitMutationResult(ctx, tx, plan.Guard, current.ID, generation, out)
}

// ApplyFixture guards passive staging and ingestion writes with the same
// provider/fixture policy composition. It never interprets a missing event
// array as an empty live event inventory.
func (r *ProviderMutationRepo) ApplyFixture(ctx context.Context, plan providerintegrity.FixtureMutationPlan) (providerintegrity.MutationResult, error) {
	var out providerintegrity.MutationResult
	if !r.mode.Valid() {
		return out, fmt.Errorf("invalid provider enforcement mode %q", r.mode)
	}
	normalizeFixtureMutationPlan(&plan)
	if err := validateFixtureMutationPlan(plan); err != nil {
		return out, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return out, fmt.Errorf("pg.ProviderMutationRepo.ApplyFixture: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, generation, providerPolicy, err := lockProviderMutationPolicy(ctx, tx)
	if err != nil {
		return out, err
	}
	if err := lockFixtureMutation(ctx, tx, plan.Fixture.ID); err != nil {
		return out, err
	}
	if receipt, found, err := loadMutationReceipt(ctx, tx, plan.Guard, plan.Fixture.ID); err != nil {
		return out, err
	} else if found {
		return receipt, tx.Commit(ctx)
	}

	current, getErr := lockedFixture(ctx, tx, plan.Fixture.ID)
	if getErr != nil && !errors.Is(getErr, fixture.ErrNotFound) {
		return out, getErr
	}
	if plan.BaselineExists != (current != nil) ||
		(current != nil && (!sameOptionalTime(current.LastPolledAt, plan.BaselineLastPolled) ||
			!newerObservation(plan.Guard.ObservedAt, current.LastPolledAt))) {
		return r.commitStaleReceipt(ctx, tx, plan.Guard, plan.Fixture.ID, current, generation)
	}
	fixturePolicy := providerintegrity.PolicyTrusted
	if current != nil {
		health, err := loadFixtureHealth(ctx, tx, current.ID)
		if err != nil {
			return out, err
		}
		fixturePolicy = providerintegrity.FixtureHealthPolicy(health)
	}
	decision, policy := providerintegrity.DecideMutation(r.mode, providerPolicy, fixturePolicy, plan.LocalPolicy)
	decision.Writer = plan.Mode
	decision.Restricted = restrictedFixtureOperations(plan, decision.RecommendedPolicy)
	out.Decision = &decision
	out.Policy = policy
	if policy == providerintegrity.PolicyRejected {
		out.Fixture = current
		return r.commitMutationResult(ctx, tx, plan.Guard, plan.Fixture.ID, generation, out)
	}

	target := cloneFixture(plan.Fixture)
	if current == nil {
		if err := insertFixture(ctx, tx, target); err != nil {
			return out, err
		}
		out.Applied, out.FixtureChanged = true, true
	} else {
		if policy == providerintegrity.PolicyPositiveOnly {
			target = mergePositiveFixture(current, target)
			if plan.Mode == providerintegrity.MutationModeStaging && plan.Activate {
				target.State = plan.Fixture.State
				target.ActivatedAt = plan.Fixture.ActivatedAt
			}
		}
		switch plan.Mode {
		case providerintegrity.MutationModeIngest:
			target.State = current.State
			target.ActivatedAt, target.CompletedAt = current.ActivatedAt, current.CompletedAt
			target.TerminalObservedAt = current.TerminalObservedAt
			if err := updateIngestFixtureTx(ctx, tx, target); err != nil {
				return out, err
			}
		case providerintegrity.MutationModeStaging:
			if current.State != fixture.StateStaging {
				return r.commitStaleReceipt(ctx, tx, plan.Guard, current.ID, current, generation)
			}
			if plan.Activate {
				if plan.ActivationAudit == nil || !plan.ActivationAudit.Valid() {
					return out, fmt.Errorf("activation audit is required")
				}
				if err := activateStagingFixtureTx(ctx, tx, target, *plan.ActivationAudit); err != nil {
					return out, err
				}
			} else if err := updateStagingFixtureTx(ctx, tx, target); err != nil {
				return out, err
			}
		default:
			return out, fmt.Errorf("unsupported fixture mutation mode %q", plan.Mode)
		}
		out.Applied = true
		out.FixtureChanged = fixturePubliclyChanged(current, target)
	}
	out.Fixture, err = lockedFixture(ctx, tx, plan.Fixture.ID)
	if err != nil {
		return out, err
	}
	classifyMutationFixtureChanges(&out, current)
	return r.commitMutationResult(ctx, tx, plan.Guard, plan.Fixture.ID, generation, out)
}

func validateActiveMutationPlan(plan providerintegrity.ActiveMutationPlan) error {
	if err := plan.Guard.Validate(); err != nil {
		return err
	}
	if plan.Fixture == nil || plan.Fixture.ID <= 0 || plan.WorkflowID == "" || !plan.LocalPolicy.Valid() || !plan.Guard.RequireHealthReceipt {
		return fmt.Errorf("invalid guarded active mutation plan")
	}
	if plan.Fixture.LastPolledAt == nil || !plan.Fixture.LastPolledAt.Equal(plan.Guard.ObservedAt) {
		return fmt.Errorf("active fixture observation does not match guard")
	}
	return plan.Fixture.ValidateInvariants()
}

func validateFixtureMutationPlan(plan providerintegrity.FixtureMutationPlan) error {
	if err := plan.Guard.Validate(); err != nil {
		return err
	}
	if plan.Fixture == nil || plan.Fixture.ID <= 0 || !plan.LocalPolicy.Valid() || plan.Guard.RequireHealthReceipt {
		return fmt.Errorf("invalid guarded fixture mutation plan")
	}
	if plan.Mode != providerintegrity.MutationModeIngest && plan.Mode != providerintegrity.MutationModeStaging {
		return fmt.Errorf("invalid fixture mutation mode")
	}
	if plan.Fixture.LastPolledAt == nil || !plan.Fixture.LastPolledAt.Equal(plan.Guard.ObservedAt) {
		return fmt.Errorf("fixture observation does not match guard")
	}
	return plan.Fixture.ValidateInvariants()
}

func normalizeActiveMutationPlan(plan *providerintegrity.ActiveMutationPlan) {
	plan.Guard.ObservedAt = plan.Guard.ObservedAt.UTC().Truncate(time.Microsecond)
	plan.BaselineLastPolled = normalizedTimeCopy(plan.BaselineLastPolled)
	if plan.Fixture != nil {
		plan.Fixture = cloneFixture(plan.Fixture)
		plan.Fixture.LastPolledAt = normalizedTimeCopy(plan.Fixture.LastPolledAt)
	}
}

func normalizeFixtureMutationPlan(plan *providerintegrity.FixtureMutationPlan) {
	plan.Guard.ObservedAt = plan.Guard.ObservedAt.UTC().Truncate(time.Microsecond)
	plan.BaselineLastPolled = normalizedTimeCopy(plan.BaselineLastPolled)
	if plan.Fixture != nil {
		plan.Fixture = cloneFixture(plan.Fixture)
		plan.Fixture.LastPolledAt = normalizedTimeCopy(plan.Fixture.LastPolledAt)
	}
}

func normalizedTimeCopy(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := value.UTC().Truncate(time.Microsecond)
	return &copy
}

func newerObservation(observedAt time.Time, baseline *time.Time) bool {
	return baseline == nil || observedAt.After(*baseline)
}

func lockProviderMutationPolicy(ctx context.Context, tx pgx.Tx) (time.Time, int64, providerintegrity.MutationPolicy, error) {
	var at *time.Time
	var generation int64
	var policy providerintegrity.MutationPolicy
	if err := tx.QueryRow(ctx, `SELECT last_observed_at,generation,policy FROM provider_health WHERE id=1 FOR SHARE`).Scan(&at, &generation, &policy); err != nil {
		return time.Time{}, 0, "", err
	}
	if !policy.Valid() {
		return time.Time{}, 0, "", fmt.Errorf("invalid stored provider mutation policy %q", policy)
	}
	if at == nil {
		return time.Time{}, generation, policy, nil
	}
	return at.UTC(), generation, policy, nil
}

func lockFixtureMutation(ctx context.Context, tx pgx.Tx, fixtureID int64) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, fixtureID)
	return err
}

func lockedFixture(ctx context.Context, tx pgx.Tx, id int64) (*fixture.Fixture, error) {
	return scanFixture(tx.QueryRow(ctx, `SELECT `+fixtureColumns+` FROM fixtures WHERE id=$1 FOR UPDATE`, id))
}

func loadMutationReceipt(ctx context.Context, tx pgx.Tx, guard providerintegrity.ObservationGuard, fixtureID int64) (providerintegrity.MutationResult, bool, error) {
	var out providerintegrity.MutationResult
	var at time.Time
	var data []byte
	err := tx.QueryRow(ctx, `SELECT observed_at,result FROM provider_mutation_receipts WHERE observation_id=$1 AND fixture_id=$2`, guard.ObservationID, fixtureID).Scan(&at, &data)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, false, nil
	}
	if err != nil {
		return out, false, err
	}
	if !at.Equal(guard.ObservedAt) {
		return out, false, fmt.Errorf("provider mutation observation ID reused with another time")
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, false, err
	}
	return out, true, nil
}

func (r *ProviderMutationRepo) commitStaleReceipt(ctx context.Context, tx pgx.Tx, guard providerintegrity.ObservationGuard, fixtureID int64, current *fixture.Fixture, generation int64) (providerintegrity.MutationResult, error) {
	out := providerintegrity.MutationResult{Stale: true, Fixture: current}
	return r.commitMutationResult(ctx, tx, guard, fixtureID, generation, out)
}

func (r *ProviderMutationRepo) commitMutationResult(ctx context.Context, tx pgx.Tx, guard providerintegrity.ObservationGuard, fixtureID, generation int64, out providerintegrity.MutationResult) (providerintegrity.MutationResult, error) {
	if fixtureID <= 0 && out.Fixture != nil {
		fixtureID = out.Fixture.ID
	}
	if fixtureID <= 0 {
		return out, fmt.Errorf("provider mutation receipt requires fixture")
	}
	data, err := json.Marshal(out)
	if err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO provider_mutation_receipts
		(observation_id,fixture_id,observed_at,health_generation,result)
		VALUES ($1,$2,$3,$4,$5) ON CONFLICT (observation_id,fixture_id) DO NOTHING`,
		guard.ObservationID, fixtureID, guard.ObservedAt, generation, data); err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM provider_mutation_receipts WHERE (observation_id,fixture_id) IN (
		SELECT observation_id,fixture_id FROM provider_mutation_receipts WHERE observed_at < $1 ORDER BY observed_at LIMIT 128
	)`, guard.ObservedAt.Add(-providerintegrity.HealthReceiptRetention)); err != nil {
		return out, err
	}
	if err := tx.Commit(ctx); err != nil {
		return out, err
	}
	return out, nil
}

func cloneFixture(in *fixture.Fixture) *fixture.Fixture {
	out := *in
	return &out
}

func mergePositiveFixture(current, observed *fixture.Fixture) *fixture.Fixture {
	out := cloneFixture(current)
	out.LastPolledAt = observed.LastPolledAt
	comparison := providerintegrity.AssessFixture(providerintegrity.FixtureComparison{
		Stored: fixtureMutationFacts(current), Observed: fixtureMutationFacts(observed),
	})
	blocksClock := slices.Contains(comparison.Reasons, providerintegrity.ReasonPhaseRegressed) ||
		slices.Contains(comparison.Reasons, providerintegrity.ReasonTerminalRegressed) ||
		slices.Contains(comparison.Reasons, providerintegrity.ReasonClockRegressed)
	if !blocksClock {
		out.APIStatus = observed.APIStatus
		if out.APIStatus.Long == "" && out.APIStatus.Short == current.APIStatus.Short {
			out.APIStatus.Long = current.APIStatus.Long
		}
		// An omitted clock is not evidence that its last known value vanished.
		// A supplied clock may clear extra at a legitimate period transition.
		if observed.APIElapsed != nil {
			out.APIElapsed, out.APIExtra = observed.APIElapsed, observed.APIExtra
		}
	}
	out.HomeScore = monotonicInt(current.HomeScore, observed.HomeScore)
	out.AwayScore = monotonicInt(current.AwayScore, observed.AwayScore)
	out.HomePenalty = monotonicInt(current.HomePenalty, observed.HomePenalty)
	out.AwayPenalty = monotonicInt(current.AwayPenalty, observed.AwayPenalty)
	// Winner flags are a projection of the retained scores, not monotonic
	// facts: a safe equalizer must clear the previous leader.
	out.UpdateResult(fillBool(current.HomeWinner, observed.HomeWinner), fillBool(current.AwayWinner, observed.AwayWinner))
	if current.Home.ID == observed.Home.ID && current.Home.Name == "" && observed.Home.Name != "" {
		out.Home.Name = observed.Home.Name
	}
	if current.Away.ID == observed.Away.ID && current.Away.Name == "" && observed.Away.Name != "" {
		out.Away.Name = observed.Away.Name
	}
	if current.League.ID == observed.League.ID {
		if out.League.Name == "" {
			out.League.Name = observed.League.Name
		}
		if out.League.Country == "" {
			out.League.Country = observed.League.Country
		}
		if out.League.Round == "" {
			out.League.Round = observed.League.Round
		}
	}
	// A restricted observation cannot start or reset the completion clock.
	out.TerminalObservedAt = current.TerminalObservedAt
	return out
}

func fixtureMutationFacts(f *fixture.Fixture) providerintegrity.FixtureFacts {
	return providerintegrity.FixtureFacts{
		FixtureID: f.ID, HomeID: f.Home.ID, AwayID: f.Away.ID, LeagueID: f.League.ID,
		HomeName: f.Home.Name, AwayName: f.Away.Name, LeagueName: f.League.Name,
		Status: string(f.APIStatus.Short), Terminal: f.APIStatus.Terminal(), Elapsed: f.APIElapsed, Extra: f.APIExtra,
		HomeScore: f.HomeScore, AwayScore: f.AwayScore,
	}
}

func monotonicInt(current, observed *int) *int {
	if observed == nil || (current != nil && *observed < *current) {
		return current
	}
	return observed
}

func fillBool(current, observed *bool) *bool {
	if current != nil || observed == nil {
		return current
	}
	return observed
}

func sameOptionalTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Equal(*right)
}

func fixturePubliclyChanged(before, after *fixture.Fixture) bool {
	if before == nil || after == nil {
		return before != after
	}
	left, right := *before, *after
	left.LastPolledAt, right.LastPolledAt = nil, nil
	left.UpdatedAt, right.UpdatedAt = time.Time{}, time.Time{}
	return !reflect.DeepEqual(left, right)
}

// classifyMutationFixtureChanges records notification effects before the
// original canonical baseline is lost. Retry receipts preserve these flags
// even when planning reads now see the committed snapshot.
func classifyMutationFixtureChanges(out *providerintegrity.MutationResult, before *fixture.Fixture) {
	if out.Fixture == nil {
		return
	}
	out.FixtureChanged = fixturePubliclyChanged(before, out.Fixture)
	if before == nil {
		out.FixtureStructuralChanged = true
		return
	}
	after := out.Fixture
	previous := fixturepresentation.From(before.APIStatus, before.APIElapsed, before.APIExtra)
	current := fixturepresentation.From(after.APIStatus, after.APIElapsed, after.APIExtra)
	out.FixturePresentationChanged = !previous.Equal(current)
	left, right := *before, *after
	left.APIStatus, right.APIStatus = fixture.APIStatus{}, fixture.APIStatus{}
	left.APIElapsed, right.APIElapsed = nil, nil
	left.APIExtra, right.APIExtra = nil, nil
	left.LastPolledAt, right.LastPolledAt = nil, nil
	left.UpdatedAt, right.UpdatedAt = time.Time{}, time.Time{}
	out.FixtureStructuralChanged = previous.PresentationState != current.PresentationState || !reflect.DeepEqual(left, right)
}

func updateActiveFixtureTx(ctx context.Context, tx pgx.Tx, f *fixture.Fixture) error {
	_, err := tx.Exec(ctx, `UPDATE fixtures SET
		api_status_short=$2,api_status_long=$3,api_elapsed=$4,api_extra=$5,kickoff=$6,
		home_team_id=$7,home_team_name=$8,away_team_id=$9,away_team_name=$10,
		league_id=$11,league_name=$12,league_season=$13,league_country=$14,league_round=$15,
		home_score=$16,away_score=$17,home_winner=$18,away_winner=$19,
		terminal_observed_at=$20,last_polled_at=$21,home_penalty=$22,away_penalty=$23
		WHERE id=$1 AND state='active'`, f.ID, f.APIStatus.Short, f.APIStatus.Long, f.APIElapsed, f.APIExtra,
		f.Kickoff, f.Home.ID, f.Home.Name, f.Away.ID, f.Away.Name, f.League.ID, f.League.Name,
		f.League.Season, f.League.Country, f.League.Round, f.HomeScore, f.AwayScore,
		f.HomeWinner, f.AwayWinner, f.TerminalObservedAt, f.LastPolledAt, f.HomePenalty, f.AwayPenalty)
	return err
}

func updateIngestFixtureTx(ctx context.Context, tx pgx.Tx, f *fixture.Fixture) error {
	_, err := tx.Exec(ctx, `UPDATE fixtures SET
		api_status_short=$2,api_status_long=$3,api_elapsed=$4,api_extra=$5,kickoff=$6,
		home_team_id=$7,home_team_name=$8,away_team_id=$9,away_team_name=$10,
		league_id=$11,league_name=$12,league_season=$13,home_score=$14,away_score=$15,
		home_winner=$16,away_winner=$17,last_polled_at=$18,league_country=$19,league_round=$20,
		home_penalty=$21,away_penalty=$22 WHERE id=$1`, f.ID, f.APIStatus.Short, f.APIStatus.Long,
		f.APIElapsed, f.APIExtra, f.Kickoff, f.Home.ID, f.Home.Name, f.Away.ID, f.Away.Name,
		f.League.ID, f.League.Name, f.League.Season, f.HomeScore, f.AwayScore, f.HomeWinner,
		f.AwayWinner, f.LastPolledAt, f.League.Country, f.League.Round, f.HomePenalty, f.AwayPenalty)
	return err
}

func updateStagingFixtureTx(ctx context.Context, tx pgx.Tx, f *fixture.Fixture) error {
	_, err := tx.Exec(ctx, `UPDATE fixtures SET api_status_short=$2,api_status_long=$3,kickoff=$4,
		home_team_id=$5,home_team_name=$6,away_team_id=$7,away_team_name=$8,
		league_id=$9,league_name=$10,league_season=$11,league_country=$12,league_round=$13,last_polled_at=$14
		WHERE id=$1 AND state='staging'`, f.ID, f.APIStatus.Short, f.APIStatus.Long, f.Kickoff,
		f.Home.ID, f.Home.Name, f.Away.ID, f.Away.Name, f.League.ID, f.League.Name,
		f.League.Season, f.League.Country, f.League.Round, f.LastPolledAt)
	return err
}

func activateStagingFixtureTx(ctx context.Context, tx pgx.Tx, f *fixture.Fixture, record auditlog.Record) error {
	if f.State != fixture.StateActive || f.ActivatedAt == nil || record.Kind() != auditlog.KindFixtureActivated || record.FixtureID() != f.ID {
		return fmt.Errorf("invalid guarded fixture activation")
	}
	if _, err := tx.Exec(ctx, `UPDATE fixtures SET state='active',activated_at=$2,
		api_status_short=$3,api_status_long=$4,kickoff=$5,
		home_team_id=$6,home_team_name=$7,away_team_id=$8,away_team_name=$9,
		league_id=$10,league_name=$11,league_season=$12,league_country=$13,league_round=$14,last_polled_at=$15
		WHERE id=$1 AND state='staging'`, f.ID, f.ActivatedAt, f.APIStatus.Short, f.APIStatus.Long,
		f.Kickoff, f.Home.ID, f.Home.Name, f.Away.ID, f.Away.Name, f.League.ID, f.League.Name,
		f.League.Season, f.League.Country, f.League.Round, f.LastPolledAt); err != nil {
		return err
	}
	return insertAuditLog(ctx, tx, record)
}

// validateActiveEventScope turns planner assumptions into repository
// invariants. Existing event rows are locked once before any fixture or event
// mutation so a malformed plan cannot cross fixture ownership boundaries.
func validateActiveEventScope(ctx context.Context, tx pgx.Tx, fixtureID int64, plan providerintegrity.ActiveMutationPlan, policy providerintegrity.MutationPolicy) error {
	for _, op := range plan.Inserts {
		if op.Event == nil || op.Event.FixtureID != fixtureID {
			return fmt.Errorf("planned event insert does not belong to fixture %d", fixtureID)
		}
		if op.Audit != nil && (op.Audit.FixtureID() != fixtureID || op.Audit.EventID() != op.Event.ID) {
			return fmt.Errorf("planned event insert audit does not belong to fixture %d", fixtureID)
		}
	}

	ids := make([]uuid.UUID, 0, len(plan.Presences)+len(plan.Updates)+len(plan.Absences)+len(plan.UnknownDeletes))
	seen := make(map[uuid.UUID]struct{}, cap(ids))
	updateKeys := make(map[uuid.UUID]string, len(plan.Updates))
	add := func(id uuid.UUID) {
		if _, exists := seen[id]; !exists {
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	for _, op := range plan.Presences {
		if op.Audit.FixtureID() != fixtureID || op.Audit.EventID() != op.EventID {
			return fmt.Errorf("planned event presence audit does not belong to fixture %d", fixtureID)
		}
		add(op.EventID)
	}
	for _, op := range plan.Updates {
		if op.Fresh == nil || op.Fresh.FixtureID != fixtureID {
			return fmt.Errorf("planned event update does not belong to fixture %d", fixtureID)
		}
		updateKeys[op.EventID] = op.Fresh.NaturalKey
		add(op.EventID)
	}
	if policy == providerintegrity.PolicyTrusted {
		for _, op := range plan.Absences {
			if op.Audit.FixtureID() != fixtureID || op.Audit.EventID() != op.EventID {
				return fmt.Errorf("planned event absence audit does not belong to fixture %d", fixtureID)
			}
			add(op.EventID)
		}
		for _, id := range plan.UnknownDeletes {
			add(id)
		}
	}
	for _, id := range ids {
		var owner int64
		var naturalKey string
		if err := tx.QueryRow(ctx, `SELECT fixture_id,natural_key FROM events WHERE id=$1 FOR UPDATE`, id).Scan(&owner, &naturalKey); err != nil {
			return fmt.Errorf("lock planned event %s: %w", id, err)
		}
		if owner != fixtureID {
			return fmt.Errorf("planned event %s belongs to fixture %d, not %d", id, owner, fixtureID)
		}
		if expected, update := updateKeys[id]; update && expected != naturalKey {
			return fmt.Errorf("planned event %s identity changed", id)
		}
	}
	return nil
}

func insertPlannedEvent(ctx context.Context, tx pgx.Tx, workflowID string, op providerintegrity.PlannedEventInsert) error {
	if op.Event == nil || op.Event.FixtureID <= 0 || workflowID == "" {
		return fmt.Errorf("invalid planned event insert")
	}
	if err := op.Event.ValidateInvariants(); err != nil {
		return err
	}
	telemetry, err := marshalTelemetry(op.Event.Telemetry)
	if err != nil {
		return err
	}
	initial := 1
	if !op.Event.Player.Known() {
		initial = 0
	}
	if _, err := tx.Exec(ctx, `INSERT INTO events (
		id,fixture_id,natural_key,event_type,detail,team_id,team_name,player_id,player_name,
		assist_id,assist_name,minute,extra,first_seen_at,debounce_count,downstream_triggered,
		monitor_complete,download_complete,removed,removed_reason,removed_at,telemetry)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,FALSE,$16,$17,$18,$19,$20,$21)`,
		op.Event.ID, op.Event.FixtureID, op.Event.NaturalKey, string(op.Event.Type), op.Event.Detail,
		op.Event.Team.ID, op.Event.Team.Name, op.Event.Player.ID, op.Event.Player.Name,
		op.Event.Assist.ID, op.Event.Assist.Name, op.Event.Minute, op.Event.Extra, op.Event.FirstSeenAt,
		initial, op.Event.MonitorComplete, op.Event.DownloadComplete, op.Event.Removed,
		op.Event.RemovedReason, op.Event.RemovedAt, telemetry); err != nil {
		return err
	}
	if initial > 0 {
		if _, err := tx.Exec(ctx, `INSERT INTO event_monitor_workflows(event_id,workflow_id) VALUES ($1,$2)`, op.Event.ID, workflowID); err != nil {
			return err
		}
	}
	if op.Audit != nil {
		if initial == 0 || !op.Audit.Valid() || op.Audit.Kind() != auditlog.KindEventDetected || op.Audit.EventID() != op.Event.ID {
			return fmt.Errorf("invalid event detected audit")
		}
		return insertAuditLog(ctx, tx, *op.Audit)
	}
	return nil
}

func registerPresenceTx(ctx context.Context, tx pgx.Tx, workflowID string, op providerintegrity.PlannedEventPresence) (bool, error) {
	var inserted bool
	err := tx.QueryRow(ctx, `INSERT INTO event_monitor_workflows(event_id,workflow_id) VALUES ($1,$2)
		ON CONFLICT DO NOTHING RETURNING true`, op.EventID, workflowID).Scan(&inserted)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	var count int
	var already bool
	if err := tx.QueryRow(ctx, `UPDATE events SET debounce_count=LEAST(debounce_count+1,3)
		WHERE id=$1 AND NOT removed RETURNING debounce_count,downstream_triggered`, op.EventID).Scan(&count, &already); err != nil {
		return false, err
	}
	if count != 3 || already {
		return false, nil
	}
	var flipped bool
	err = tx.QueryRow(ctx, `UPDATE events SET downstream_triggered=TRUE WHERE id=$1 AND NOT downstream_triggered RETURNING true`, op.EventID).Scan(&flipped)
	if err != nil {
		return false, err
	}
	if !op.Audit.Valid() || op.Audit.Kind() != auditlog.KindEventStable || op.Audit.EventID() != op.EventID {
		return false, fmt.Errorf("invalid event stable audit")
	}
	if err := insertAuditLog(ctx, tx, op.Audit); err != nil {
		return false, err
	}
	workflow := fmt.Sprintf("event-%s", op.EventID)
	if _, err := tx.Exec(ctx, `INSERT INTO event_downstream_workflows(event_id,workflow_type,workflow_id)
		VALUES ($1,'discovery',$2) ON CONFLICT DO NOTHING`, op.EventID, workflow); err != nil {
		return false, err
	}
	return flipped, nil
}

func updateEventFieldsTx(ctx context.Context, tx pgx.Tx, op providerintegrity.PlannedEventUpdate, policy providerintegrity.MutationPolicy) (bool, error) {
	if op.Fresh == nil {
		return false, fmt.Errorf("nil event update")
	}
	if policy == providerintegrity.PolicyTrusted {
		result, execErr := tx.Exec(ctx, `UPDATE events SET assist_id=$2,assist_name=$3,minute=$4,extra=$5,detail=$6
			WHERE id=$1 AND (assist_id IS DISTINCT FROM $2 OR assist_name IS DISTINCT FROM $3 OR minute IS DISTINCT FROM $4 OR extra IS DISTINCT FROM $5 OR detail IS DISTINCT FROM $6)`,
			op.EventID, op.Fresh.Assist.ID, op.Fresh.Assist.Name, op.Fresh.Minute, op.Fresh.Extra, op.Fresh.Detail)
		if execErr != nil {
			return false, execErr
		}
		return result.RowsAffected() == 1, nil
	}
	result, err := tx.Exec(ctx, `UPDATE events SET assist_id=COALESCE(assist_id,$2),assist_name=COALESCE(assist_name,$3)
		WHERE id=$1 AND ((assist_id IS NULL AND $2::int IS NOT NULL) OR (assist_name IS NULL AND $3::text IS NOT NULL))`,
		op.EventID, op.Fresh.Assist.ID, op.Fresh.Assist.Name)
	if err != nil {
		return false, err
	}
	return result.RowsAffected() == 1, nil
}

func registerAbsenceTx(ctx context.Context, tx pgx.Tx, workflowID string, op providerintegrity.PlannedEventAbsence) (bool, error) {
	var inserted bool
	err := tx.QueryRow(ctx, `INSERT INTO event_drop_workflows(event_id,workflow_id) VALUES ($1,$2)
		ON CONFLICT DO NOTHING RETURNING true`, op.EventID, workflowID).Scan(&inserted)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	var count int
	var removed bool
	if err := tx.QueryRow(ctx, `UPDATE events SET debounce_count=GREATEST(debounce_count-1,0)
		WHERE id=$1 RETURNING debounce_count,removed`, op.EventID).Scan(&count, &removed); err != nil {
		return false, err
	}
	if count != 0 || removed {
		return false, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE events SET removed=TRUE,removed_reason='var',removed_at=NOW()
		WHERE id=$1 AND NOT removed`, op.EventID); err != nil {
		return false, err
	}
	if _, err := terminalizePendingCandidatesForRemovedEvent(ctx, tx, op.EventID, "", time.Now().UTC()); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE event_downstream_workflows SET completed_at=NOW(),outcome_class=$2
		WHERE event_id=$1 AND completed_at IS NULL`, op.EventID, string(event.OutcomeEventRemoved)); err != nil {
		return false, err
	}
	if !op.Audit.Valid() || op.Audit.Kind() != auditlog.KindEventRemoved || op.Audit.EventID() != op.EventID {
		return false, fmt.Errorf("invalid event removal audit")
	}
	if err := insertAuditLog(ctx, tx, op.Audit); err != nil {
		return false, err
	}
	return true, nil
}

func completeFixtureIfReadyTx(ctx context.Context, tx pgx.Tx, f *fixture.Fixture, plan providerintegrity.CompletionPlan) (bool, error) {
	if plan.TerminalBefore.IsZero() || plan.CompletedAt.IsZero() || plan.GraceSeconds <= 0 {
		return false, nil
	}
	var ready bool
	var parity *bool
	err := tx.QueryRow(ctx, `SELECT
		f.state='active' AND f.api_status_short IN ('ft','aet','pen','canc','abd','wo','awd')
		AND f.terminal_observed_at IS NOT NULL AND f.terminal_observed_at <= $2
		AND NOT EXISTS (SELECT 1 FROM events e WHERE e.fixture_id=f.id AND NOT e.removed AND NOT e.downstream_triggered AND e.debounce_count>0)
		AND NOT EXISTS (SELECT 1 FROM event_downstream_workflows d JOIN events e ON e.id=d.event_id WHERE e.fixture_id=f.id AND d.completed_at IS NULL),
		CASE WHEN f.api_status_short IN ('canc','abd','wo','awd') THEN NULL ELSE
			f.home_score IS NOT NULL AND f.away_score IS NOT NULL
			AND f.home_score=(SELECT COUNT(*) FROM events e WHERE e.fixture_id=f.id AND e.event_type='goal' AND e.team_id=f.home_team_id AND NOT e.removed)
			AND f.away_score=(SELECT COUNT(*) FROM events e WHERE e.fixture_id=f.id AND e.event_type='goal' AND e.team_id=f.away_team_id AND NOT e.removed) END
		FROM fixtures f WHERE f.id=$1`, f.ID, plan.TerminalBefore).Scan(&ready, &parity)
	if err != nil || !ready {
		return false, err
	}
	if f.TerminalObservedAt == nil {
		return false, fmt.Errorf("completion-ready fixture lacks terminal observation")
	}
	var penaltyDecided *bool
	if f.APIStatus.Short == apifootball.StatusPenaltyDone {
		decided := f.HomePenalty != nil && f.AwayPenalty != nil && *f.HomePenalty != *f.AwayPenalty
		penaltyDecided = &decided
	}
	record, err := auditlog.New(auditlog.KindFixtureCompleted, uuid.Nil, f.ID, auditlog.FixtureCompletedPayload{
		FixtureID: f.ID, TerminalObservedAt: *f.TerminalObservedAt, CompletedAt: plan.CompletedAt,
		GraceSeconds: plan.GraceSeconds, ProviderScoreEventParity: plan.ProviderScoreEventParity,
		DurableScoreEventParity: parity, PenaltyResultDecided: penaltyDecided,
	})
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE fixtures SET state='completed',completed_at=$2 WHERE id=$1 AND state='active'`, f.ID, plan.CompletedAt); err != nil {
		return false, err
	}
	if err := insertAuditLog(ctx, tx, record); err != nil {
		return false, err
	}
	return true, nil
}
