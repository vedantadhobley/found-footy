// Package monitor holds the Temporal activities that the two poll
// workflows orchestrate.
//
// ActivePollWorkflow (fires every 30s) uses:
//  1. ActivateUpcoming — DB-only check; promotes staging fixtures
//     whose stored kickoff is within the activation window.
//  2. ListActiveFixtureIDs — cheap ID pull.
//  3. FetchLiveFixtures — one batched /fixtures?ids= call.
//  4. ReconcileFixture — per fixture, refresh row + diff events +
//     vote presence/absence for each event. Concurrent via
//     workflow.Go in the coordinator.
//
// StagingPollWorkflow (fires per cron schedule, default 15 min) uses:
//  1. PollStagingFixtures — API poll of ALL staging fixtures.
//     Catches vendor-side postponements, kickoff corrections, and
//     early starts (Live() status → emergency activation). Also
//     re-checks ShouldActivateNow after applying any vendor kickoff
//     correction so a corrected kickoff inside the activation window
//     triggers activation the same tick.
//
// The two workflows run on independent Temporal Schedules — the
// staging cadence can be tuned at runtime with `temporal schedule
// update staging-poll-scheduled --cron ...` without a redeploy. See
// decisions.md 2026-07-10 workflow-split entry.
//
// Debounce model per decisions.md 2026-07-07 symmetric-counter entry.
// NATS emissions (via the event composer) and EventWorkflow spawn (via
// DownstreamSpawner, on the downstream_triggered flip) both SHIPPED in O3.
package monitor

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/domain/event"
	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
	eventinfra "github.com/vedantadhobley/found-footy/internal/infra/event"
)

// fixtureFetcher is the narrow interface the Monitor activities need
// from the apifootball adapter — same idiom as ingest's fetcher. The
// (fixtures, failedIDs, err) return shape carries partial-failure info
// per apifootball.ListFixturesByIDs — err is set only on catastrophic
// failure, failedIDs lists the IDs that didn't come back.
type fixtureFetcher interface {
	ListFixturesByIDs(ctx context.Context, ids []int64) (
		fixtures []apifootball.APIFixture, failedIDs []int64, err error,
	)
}

// Activities bundles the deps every monitor activity needs. Now is
// injectable per the harness discipline (docs/decisions.md
// 2026-07-08 test corpus entry).
type Activities struct {
	APIFootball fixtureFetcher
	FixtureRepo fixture.Repo
	EventRepo   event.Repo

	// Composer — durable Postgres audit helper for semantic event emissions
	// (fixture.activated, fixture.completed, event.detected,
	// event.stable, event.removed, event.rank_recalculated). Live NATS
	// signals use separate activities. May be
	// nil in older tests that don't wire it; emit calls no-op on nil.
	Composer eventComposer

	// Spawner — starts downstream workflows (Discovery for now) via
	// Temporal. Bundled with the row-insert into
	// event_downstream_workflows in the same activity so the
	// completion check sees the pending row before the spawned
	// workflow lands its first activity (2026-07-16 spawn rule).
	// May be nil in tests that only exercise emission paths.
	Spawner DownstreamSpawner

	// ActivationWindow — kickoff-lookahead used by both
	// ActivateUpcoming (DB-only check every 30s) and
	// PollStagingFixtures (API poll each staging tick, for the
	// "vendor pushed corrected kickoff into window" case). Sourced
	// from config.Workflows.ActivationWindow at worker startup.
	ActivationWindow time.Duration

	// TerminalGracePeriod is the minimum uninterrupted terminal observation
	// window before an active fixture can move to completed.
	TerminalGracePeriod time.Duration

	// FleetEnabled mirrors config.FirefoxFleetConfig.Enabled (#160). Set
	// at worker init; surfaced to ActivePollWorkflow via GetMonitorConfig
	// so it provisions/releases Firefox instances only when the fleet is on.
	FleetEnabled bool

	Now func() time.Time
}

// eventComposer narrows the *eventinfra.Composer surface Monitor calls
// to exactly the verbs the activity needs. Prod wires the concrete
// pointer directly; tests inject fakes.
type eventComposer interface {
	Publish(ctx context.Context, kind eventinfra.Kind, eventID uuid.UUID, fixtureID int64, payload any) (int64, error)
}

func (a *Activities) now() time.Time {
	if a.Now != nil {
		return a.Now().UTC()
	}
	return time.Now().UTC()
}

// ── GetMonitorConfig ──────────────────────────────────────────

// GetMonitorConfigInput has no fields.
type GetMonitorConfigInput struct{}

// GetMonitorConfigOutput exposes env-driven config to the workflow.
// Mirrors the ingest.GetIngestConfig pattern for the same reason
// (workflows can't touch env directly per Temporal determinism).
type GetMonitorConfigOutput struct {
	ActivationWindow time.Duration
	FleetEnabled     bool // #160 — gate per-event Firefox provisioning/release
}

// GetMonitorConfig — trivial config accessor for the poll workflows.
// Consumed by ActivePollWorkflow's ActivateUpcoming step and
// StagingPollWorkflow's PollStagingFixtures step — both need
// ActivationWindow.
func (a *Activities) GetMonitorConfig(
	_ context.Context, _ GetMonitorConfigInput,
) (GetMonitorConfigOutput, error) {
	return GetMonitorConfigOutput{
		ActivationWindow: a.ActivationWindow,
		FleetEnabled:     a.FleetEnabled,
	}, nil
}
