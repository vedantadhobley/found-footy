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
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	discoveryactivity "github.com/vedantadhobley/found-footy/internal/activity/discovery"
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

	// Composer — dual-write helper for semantic event emissions
	// (fixture.activated, fixture.completed, event.detected,
	// event.stable, event.removed, event.rank_recalculated). May be
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

// ── ActivateUpcoming ───────────────────────────────────────

// ActivateUpcomingInput carries the lookahead window (typically 30
// min). Fixtures in staging with kickoff <= now+lookahead get promoted
// to active so the batched poll starts covering them next cycle.
type ActivateUpcomingInput struct {
	Lookahead time.Duration
}

// ActivateUpcomingOutput reports how many fixtures got promoted +
// how many were considered (for observability).
type ActivateUpcomingOutput struct {
	Considered int
	Activated  int
	Errors     []string
}

// ActivateUpcoming reads staging fixtures with kickoff before
// now+Lookahead, promotes any that ShouldActivateNow returns true for.
// Uses the same domain predicate ingest uses at first-sight
// activation, so the rule stays in one place.
func (a *Activities) ActivateUpcoming(ctx context.Context, in ActivateUpcomingInput) (ActivateUpcomingOutput, error) {
	out := ActivateUpcomingOutput{}
	now := a.now()

	candidates, err := a.FixtureRepo.ListStagingBeforeKickoff(ctx, now.Add(in.Lookahead))
	if err != nil {
		return out, fmt.Errorf("monitor.ActivateUpcoming: list: %w", err)
	}
	out.Considered = len(candidates)

	for _, f := range candidates {
		if !f.ShouldActivateNow(now, in.Lookahead) {
			continue
		}
		if err := f.Activate(now); err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("activate fixture=%d: %v", f.ID, err))
			continue
		}
		if err := a.FixtureRepo.Upsert(ctx, f); err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("upsert fixture=%d: %v", f.ID, err))
			continue
		}
		out.Activated++
		a.emitFixtureActivated(ctx, f.ID, now, "kickoff_soon")
	}
	return out, nil
}

// ── PollStagingFixtures ───────────────────────────────────────

// PollStagingFixturesInput carries the ActivationWindow the workflow
// resolved for this cycle. Passed through PollStagingFixtures'
// kickoff-correction check.
type PollStagingFixturesInput struct {
	// ActivationWindow — same value ActivePollWorkflow's ActivateUpcoming
	// uses. A staging fixture whose kickoff was corrected into this
	// window by the vendor gets activated the same tick even if the
	// API status is still NS.
	ActivationWindow time.Duration
}

// PollStagingFixturesOutput reports what happened this tick.
// Considered is the raw count of staging fixtures polled; Polled is
// how many the API returned data for; MissedIDs counts vendor-side
// chunk failures; EmergencyActivated + KickoffActivated separate the
// two activation paths so operators can see which triggered.
type PollStagingFixturesOutput struct {
	Considered         int
	Polled             int
	MissedIDs          int
	EmergencyActivated int // API status flipped to Live() while still staging
	KickoffActivated   int // vendor-corrected kickoff pulled fixture into activation window
	Errors             []string
}

// PollStagingFixtures fires a batched /fixtures?ids= call against
// EVERY staging fixture (no bucket filter — StagingPollWorkflow's
// Temporal Schedule decides when to fire, not us), then reconciles
// each response. Two activation paths:
//
//   - Live() emergency: the API says the match is already playing
//     (or paused mid-play) while we still have it in staging. Ingest
//     had the wrong kickoff, or the vendor published a corrected one,
//     or the match started early. Activate immediately.
//   - Kickoff-corrected: the vendor pushed a new kickoff time that
//     brings the fixture inside our activation window. Same fix as
//     ActivePollWorkflow's ActivateUpcoming DB-only check but
//     works on API-side mutations ingest hasn't caught yet.
//
// If neither triggers, RecordStagingPoll updates APIStatus + Kickoff
// + last_polled_at without touching last_activity_at (matches Python
// — passive polls don't count as activity for frontend sort).
//
// Called by StagingPollWorkflow on its cron cadence (default 15 min).
// Cadence tunable at runtime via `temporal schedule update
// staging-poll-scheduled --cron ...` — this activity doesn't care
// about cadence, only about doing one full poll pass per invocation.
func (a *Activities) PollStagingFixtures(ctx context.Context, in PollStagingFixturesInput) (PollStagingFixturesOutput, error) {
	out := PollStagingFixturesOutput{}
	now := a.now()

	candidates, err := a.FixtureRepo.ListByState(ctx, fixture.StateStaging)
	if err != nil {
		return out, fmt.Errorf("monitor.PollStagingFixtures: list: %w", err)
	}
	out.Considered = len(candidates)
	if len(candidates) == 0 {
		return out, nil
	}

	ids := make([]int64, len(candidates))
	for i, f := range candidates {
		ids[i] = f.ID
	}
	apiFixtures, failedIDs, err := a.APIFootball.ListFixturesByIDs(ctx, ids)
	if err != nil {
		// Catastrophic (all chunks failed / ctx cancelled) — surface the
		// error but keep the FailedIDs count so the workflow can log it.
		out.MissedIDs = len(failedIDs)
		return out, fmt.Errorf("monitor.PollStagingFixtures: fetch: %w", err)
	}
	out.MissedIDs = len(failedIDs)

	apiByID := make(map[int64]apifootball.APIFixture, len(apiFixtures))
	for _, af := range apiFixtures {
		apiByID[af.Fixture.ID] = af
	}

	for _, f := range candidates {
		af, ok := apiByID[f.ID]
		if !ok {
			// Missed this chunk — next tick picks it up naturally.
			continue
		}
		newStatus := fixture.APIStatus{
			Short: af.Fixture.Status.Short,
			Long:  af.Fixture.Status.Long,
		}
		newKickoff := af.Fixture.Date

		switch {
		case newStatus.Live():
			// Emergency — API says the match is already live.
			// Refresh the fields Activate doesn't touch (kickoff, status)
			// FIRST via RecordStagingPoll, then transition.
			f.RecordStagingPoll(newStatus, newKickoff, now)
			if err := f.Activate(now); err != nil {
				out.Errors = append(out.Errors, fmt.Sprintf("emergency activate fixture=%d: %v", f.ID, err))
				continue
			}
			out.EmergencyActivated++
			a.emitFixtureActivated(ctx, f.ID, now, "already_started")

		default:
			// Non-Live status. Update the fields, then check if the
			// (possibly-corrected) kickoff pulls us into the activation
			// window.
			f.RecordStagingPoll(newStatus, newKickoff, now)
			if f.ShouldActivateNow(now, in.ActivationWindow) {
				if err := f.Activate(now); err != nil {
					out.Errors = append(out.Errors, fmt.Sprintf("kickoff activate fixture=%d: %v", f.ID, err))
					continue
				}
				out.KickoffActivated++
				a.emitFixtureActivated(ctx, f.ID, now, "kickoff_correction")
			}
		}

		if err := a.FixtureRepo.Upsert(ctx, f); err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("upsert fixture=%d: %v", f.ID, err))
			continue
		}
		out.Polled++
	}
	return out, nil
}

// ── ListActiveFixtureIDs ──────────────────────────────────────

// ListActiveFixtureIDsOutput carries the ID slice.
type ListActiveFixtureIDsOutput struct {
	IDs []int64
}

// ListActiveFixtureIDs returns fixture IDs currently in state=active.
// Thin wrapper over fixture.Repo.ListActiveIDs — separate activity
// so it has its own timeout + retry policy in the workflow.
func (a *Activities) ListActiveFixtureIDs(ctx context.Context) (ListActiveFixtureIDsOutput, error) {
	ids, err := a.FixtureRepo.ListActiveIDs(ctx)
	if err != nil {
		return ListActiveFixtureIDsOutput{}, fmt.Errorf("monitor.ListActiveFixtureIDs: %w", err)
	}
	return ListActiveFixtureIDsOutput{IDs: ids}, nil
}

// ── FetchLiveFixtures ─────────────────────────────────────────

// FetchLiveFixturesInput carries the ID slice. The activity accepts any
// size — chunking to the vendor's 20/call cap happens inside the client
// (apifootball.ListFixturesByIDs), which fires per-chunk HTTP calls in
// parallel via goroutines.
type FetchLiveFixturesInput struct {
	IDs []int64
}

// FetchLiveFixturesOutput carries the API response array plus any IDs
// that didn't come back (partial failure). The workflow decides what
// to do with FailedIDs — Monitor logs and lets the next 30s cycle
// naturally re-request them; other callers might loop with backoff.
type FetchLiveFixturesOutput struct {
	Fixtures  []apifootball.APIFixture
	FailedIDs []int64
}

// FetchLiveFixtures calls apifootball with the given IDs. Zero-length
// input short-circuits — no round trip.
func (a *Activities) FetchLiveFixtures(ctx context.Context, in FetchLiveFixturesInput) (FetchLiveFixturesOutput, error) {
	if len(in.IDs) == 0 {
		return FetchLiveFixturesOutput{}, nil
	}
	fixtures, failedIDs, err := a.APIFootball.ListFixturesByIDs(ctx, in.IDs)
	if err != nil {
		return FetchLiveFixturesOutput{FailedIDs: failedIDs}, fmt.Errorf("monitor.FetchLiveFixtures: %w", err)
	}
	return FetchLiveFixturesOutput{Fixtures: fixtures, FailedIDs: failedIDs}, nil
}

// ── ReconcileFixture ──────────────────────────────────────────

// ReconcileFixtureInput carries one API fixture + the workflow_id
// that's voting. Called once per fixture per monitor cycle, run
// concurrently from the workflow via workflow.Go.
type ReconcileFixtureInput struct {
	APIFixture apifootball.APIFixture
	WorkflowID string
}

// ReconcileFixtureOutput reports what happened for this fixture.
// EventsBecameStable + EventsRemovedIDs are the events whose state
// transitions the caller acts on (Discovery spawn + the #172 VAR-destroy
// pipeline the poll workflow runs for each just-removed event).
type ReconcileFixtureOutput struct {
	FixtureID          int64
	NewEventsDetected  int
	EventsBecameStable []string // natural_keys of events that just crossed count=3
	// NewNamedEventIDs — UUIDs of known-player events just inserted at
	// debounce_count=1 (#160). This includes goals, red cards, and missed
	// penalties. The poll workflow provisions a Firefox instance for each so
	// it is warm by count=3. Same predicate as event.detected (Player.Known()).
	NewNamedEventIDs []uuid.UUID
	EventsRemoved    []string    // natural_keys of confirmed events that just hit count=0 (VAR)
	EventsRemovedIDs []uuid.UUID // #172: their UUIDs — the poll workflow cancels discovery + runs DestroyEvent for each
	// GoalAbsencesHeld lists stored goals that were absent from this response
	// but still required by its aggregate score. They receive no absence vote:
	// classifying them as VAR would contradict stronger provider evidence.
	GoalAbsencesHeld []string
	UnknownDropped   int // unknown-scorer placeholders hard-deleted on disappearance
	// Completed — true if this reconcile pass transitioned the fixture
	// from active → completed. See docs/design/proposals/completion-contract.md.
	Completed bool
	Errors    []string

	// ── N4 live-feed classification signals (decisions.md 2026-08-14) ──
	// Populated every cycle; the poll workflow partitions each fixture into
	// fixture.clock (ClockChanged, minute-only) vs fixture.update (Structural)
	// — disjoint, structural wins.
	Minute int  // current match minute (api elapsed) — for the clock tick payload
	Extra  *int // stoppage minutes on top of Minute, or nil
	// ClockChanged — minute/extra advanced vs the prior poll. A frozen clock
	// (HT, pre-kickoff, stalled) leaves it false → no tick that cycle.
	ClockChanged bool
	// Structural — something a consumer must full-refetch changed this cycle:
	// a new/removed/stabilized event, an unknown-scorer drop, a score/penalty/
	// winner/status change, or completion. Drives fixture.update. Set
	// incrementally (below), so it is correct at every return path.
	Structural bool
}

// ReconcileFixture is the per-fixture per-cycle work:
//  1. Refresh the fixture row (API-mutable fields + LastPolledAt).
//  2. Diff API events against pg events (including removed — for
//     collision-handling of previously-removed natural_keys).
//  3. For each API event that doesn't exist in pg AND isn't a
//     previously-removed natural_key: Insert (seeds debounce_count=1
//     + records this workflow's presence vote).
//  4. For each pg event ALSO in API: RegisterEventPresence
//     (increments count, may flip downstream_triggered).
//  5. For each pg event NOT in API: hold a goal when the aggregate score
//     proves the response omitted one; otherwise RegisterEventAbsence
//     (decrements count, may hit zero + soft-delete).
//
// Removed events are NOT voted against — they're terminal. If the
// same natural_key appears in the API for a removed event, we skip
// (see the collision-handling comment in event.Repo.Insert).
func (a *Activities) ReconcileFixture(ctx context.Context, in ReconcileFixtureInput) (ReconcileFixtureOutput, error) {
	out := ReconcileFixtureOutput{FixtureID: in.APIFixture.Fixture.ID}
	now := a.now()

	// Step 1: refresh the fixture row.
	f, err := a.FixtureRepo.Get(ctx, in.APIFixture.Fixture.ID)
	if err != nil {
		return out, fmt.Errorf("monitor.ReconcileFixture: get fixture: %w", err)
	}
	// N4: snapshot the API-mutable fields before the Update* calls so we can
	// classify this cycle as clock-only vs structural once they mutate f.
	prevStatus := f.APIStatus.Short
	prevElapsed, prevExtra := f.APIElapsed, f.APIExtra
	prevHomeScore, prevAwayScore := f.HomeScore, f.AwayScore
	prevHomeWinner, prevAwayWinner := f.HomeWinner, f.AwayWinner
	prevHomePen, prevAwayPen := f.HomePenalty, f.AwayPenalty
	// Score and event-array facts must come from the same provider response.
	// The immutable view both guards destructive goal-absence votes below and
	// decides whether this cycle is coherent enough to advance completion.
	scoreInventory := newScoreEventInventory(in.APIFixture)

	f.UpdateFromPoll(
		fixture.APIStatus{Short: in.APIFixture.Fixture.Status.Short, Long: in.APIFixture.Fixture.Status.Long},
		in.APIFixture.Fixture.Status.Elapsed,
		in.APIFixture.Fixture.Status.Extra,
		in.APIFixture.Goals.Home,
		in.APIFixture.Goals.Away,
		scoreInventory.completionVote(
			in.APIFixture.Fixture.Status.Short,
			in.APIFixture.Teams.Home.ID,
			in.APIFixture.Teams.Away.ID,
		),
		now,
	)
	// Decision-time vendor flags — present only once the result is settled, so
	// they ride alongside the poll rather than inside UpdateFromPoll. The live
	// monitor is the only path that watches a match decide. Winner is display
	// data; it cannot bypass the coherent three-poll completion debounce.
	// Penalty captures the shootout result for knockout "who won on pens".
	f.UpdateWinners(in.APIFixture.Teams.Home.Winner, in.APIFixture.Teams.Away.Winner)
	f.UpdatePenalty(in.APIFixture.Score.Penalty.Home, in.APIFixture.Score.Penalty.Away)

	// N4: classify the non-event changes now (event-driven structural signals +
	// completion set out.Structural incrementally below). ClockChanged/Minute/
	// Extra are set here so every return path carries them.
	out.Minute = derefInt(f.APIElapsed)
	out.Extra = f.APIExtra
	out.ClockChanged = intPtrChanged(prevElapsed, f.APIElapsed) || intPtrChanged(prevExtra, f.APIExtra)
	if prevStatus != f.APIStatus.Short ||
		intPtrChanged(prevHomeScore, f.HomeScore) || intPtrChanged(prevAwayScore, f.AwayScore) ||
		intPtrChanged(prevHomePen, f.HomePenalty) || intPtrChanged(prevAwayPen, f.AwayPenalty) ||
		boolPtrChanged(prevHomeWinner, f.HomeWinner) || boolPtrChanged(prevAwayWinner, f.AwayWinner) {
		out.Structural = true
	}

	if err := a.FixtureRepo.Upsert(ctx, f); err != nil {
		return out, fmt.Errorf("monitor.ReconcileFixture: upsert fixture: %w", err)
	}

	// Step 2-5: diff events. Read the complete identity history in one query.
	// Non-removed rows participate in presence/absence voting; removed rows are
	// immutable sequence tombstones that prevent brace renumbering and key reuse.
	allEvents, err := a.EventRepo.ListAllByFixture(ctx, f.ID)
	if err != nil {
		return out, fmt.Errorf("monitor.ReconcileFixture: list fixture event history: %w", err)
	}

	pgByKey := make(map[string]*event.Event, len(allEvents))
	allKeys := make(map[string]struct{}, len(allEvents))
	for _, stored := range allEvents {
		allKeys[stored.NaturalKey] = struct{}{}
		if !stored.Removed {
			pgByKey[stored.NaturalKey] = stored
		}
	}
	incompleteGoalTeams := map[int]bool{
		in.APIFixture.Teams.Home.ID: scoreInventory.scoreRequiresMissingGoal(in.APIFixture.Teams.Home.ID),
		in.APIFixture.Teams.Away.ID: scoreInventory.scoreRequiresMissingGoal(in.APIFixture.Teams.Away.ID),
	}
	eventSequences, err := assignEventSequences(in.APIFixture.Events, allEvents, incompleteGoalTeams)
	if err != nil {
		return out, fmt.Errorf("monitor.ReconcileFixture: assign event identities: %w", err)
	}

	// Build a set of API event natural_keys we've seen this cycle,
	// so absence votes for pg events NOT in the set are correct.
	apiKeys := make(map[string]struct{}, len(in.APIFixture.Events))

	for apiIndex, apiEv := range in.APIFixture.Events {
		// Filter to trackable event types via event.TrackableEventType.
		// Details:
		//   Goal with detail in {Normal Goal, Penalty, Own Goal} +
		//     no "Penalty Shootout" in comments → tracked
		//   Goal with detail Missed Penalty outside a shootout → tracked
		//   Card with detail = "Red card" → tracked
		//   Everything else (yellow cards, substitutions, VAR announcements,
		//     shootout goals/misses) → skip
		domainType := trackableType(apiEv)
		if domainType == "" {
			continue
		}
		sequence, ok := eventSequences[apiIndex]
		if !ok {
			out.Errors = append(out.Errors, fmt.Sprintf("event identity missing api_index=%d", apiIndex))
			continue
		}
		domainEv, key, err := a.buildDomainEvent(f, apiEv, domainType, sequence, now)
		if err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("build event: %v", err))
			continue
		}
		apiKeys[key] = struct{}{}

		if existing, ok := pgByKey[key]; ok {
			// Unknown-scorer placeholder: pinned at debounce 0 — no presence
			// vote, so it never climbs toward the trigger. It's superseded
			// when the vendor attributes a scorer (a new player-keyed
			// natural_key debounces up) or hard-deleted when it disappears
			// (absence loop below). Python parity: initial_count=0, empty
			// monitor_workflows. key is already in apiKeys, so absence won't
			// touch it while it's still present.
			if !domainEv.Player.Known() {
				continue
			}
			// Event exists — register presence vote.
			_, justTriggered, err := a.EventRepo.RegisterEventPresence(ctx, existing.ID, in.WorkflowID)
			if err != nil {
				out.Errors = append(out.Errors, fmt.Sprintf("presence event=%s: %v", key, err))
				continue
			}
			if justTriggered {
				out.EventsBecameStable = append(out.EventsBecameStable, key)
				out.Structural = true

				// Fast path: spawn Discovery immediately on confirmation
				// so the completion check sees "downstream pending" this
				// cycle. A spawn/register error here is non-fatal because
				// the recovery pass below (runs every cycle before the
				// completion check) re-attempts any spawn this call
				// dropped — so a transient Temporal/pg blip self-heals
				// instead of orphaning the event. See audit-2026-07-26 P1 #3.
				if err := a.registerAndSpawnEvent(ctx, existing, domainEv, in.APIFixture.Fixture.ID); err != nil {
					out.Errors = append(out.Errors, fmt.Sprintf("spawn event=%s: %v", key, err))
				}
				a.emitEventStable(ctx, existing.ID, in.APIFixture.Fixture.ID, domainEv)
			}

			// #199: re-persist provider-mutable fields that arrive/change AFTER
			// first detection — assist (API-Football fills the assister in late,
			// often post-match), minute/extra (VAR corrections), detail
			// (reclassification). Only on a real delta → UPDATE + flag Structural
			// so the fixture rides fixture.update and the consumer refetches.
			// Identity (team/player/type, the natural_key) is never touched here.
			if existing.MutableFieldsChanged(domainEv) {
				if err := a.EventRepo.UpdateMutableFields(ctx, existing.ID, domainEv); err != nil {
					out.Errors = append(out.Errors, fmt.Sprintf("update mutable event=%s: %v", key, err))
				} else {
					out.Structural = true
				}
			}
		} else if _, removedAlready := allKeys[key]; removedAlready {
			// Terminal — skip. Prior instance of this natural_key was
			// soft-removed. See package docstring.
			continue
		} else {
			// New event — Insert seeds debounce_count=1 when the player is
			// known, or 0 (placeholder, no vote) when the player is unknown.
			if err := a.EventRepo.Insert(ctx, domainEv, in.WorkflowID); err != nil {
				out.Errors = append(out.Errors, fmt.Sprintf("insert event=%s: %v", key, err))
				continue
			}
			out.NewEventsDetected++
			out.Structural = true
			allKeys[key] = struct{}{}

			// event.detected is a confirmed-detection signal for durable /
			// external consumers — don't emit for an unknown-player placeholder
			// (it may vanish and be replaced by an attributed event). The
			// known-player insert that supersedes it emits detected then.
			// External fan-out only; Composer nil in tests → no-op.
			if domainEv.Player.Known() {
				a.emitEventDetected(ctx, domainEv.ID, in.APIFixture.Fixture.ID, domainEv)
				// #160: known player at count=1 → we have all the data needed for
				// any searchable event type; warm this event's Firefox now so it
				// is ready when debounce settles and search begins at count=3.
				out.NewNamedEventIDs = append(out.NewNamedEventIDs, domainEv.ID)
			}
		}
	}

	// Absence votes: pg events NOT in the API this cycle.
	//
	// A missing non-goal event still follows the ordinary absence debounce.
	// A missing goal first passes a stronger consistency check: when the
	// aggregate score exceeds the current API goal inventory for that team,
	// the response itself proves at least one goal element is omitted. In that
	// state we conservatively retain every missing stored goal for the team;
	// choosing one would invent identity evidence the provider did not supply.
	// A true VAR drops the aggregate score, so normal absence voting resumes.
	for key, pgEv := range pgByKey {
		if _, present := apiKeys[key]; present {
			continue
		}
		// Unknown-scorer placeholder that disappeared → hard-delete
		// immediately (Python's unknown_scorer_disappeared). It was never a
		// confirmed event — usually the vendor just attributed the scorer and
		// a new player-keyed natural_key superseded it. It must NOT run the
		// soft-delete/VAR path: that would mis-stamp removed_reason='var',
		// emit a misleading event.removed, and overload the count-0 state.
		if !pgEv.Player.Known() {
			if err := a.EventRepo.DeleteUnknownEvent(ctx, pgEv.ID); err != nil {
				out.Errors = append(out.Errors, fmt.Sprintf("delete unknown event=%s: %v", key, err))
				continue
			}
			out.UnknownDropped++
			out.Structural = true
			continue
		}
		if pgEv.Type == event.TypeGoal && scoreInventory.scoreRequiresMissingGoal(pgEv.Team.ID) {
			out.GoalAbsencesHeld = append(out.GoalAbsencesHeld, key)
			continue
		}
		_, hitZero, err := a.EventRepo.RegisterEventAbsence(ctx, pgEv.ID, in.WorkflowID)
		if err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("absence event=%s: %v", key, err))
			continue
		}
		if hitZero {
			out.EventsRemoved = append(out.EventsRemoved, key)
			out.EventsRemovedIDs = append(out.EventsRemovedIDs, pgEv.ID)
			out.Structural = true
			a.emitEventRemoved(ctx, pgEv.ID, in.APIFixture.Fixture.ID, now)
		}
	}

	// Step 5.5: discovery spawn-recovery. registerAndSpawnEvent is
	// idempotent (RegisterDownstreamWorkflow is INSERT ON CONFLICT DO
	// NOTHING; SpawnEvent classifies WorkflowExecutionAlreadyStarted),
	// so re-running it every cycle is a no-op for healthy discoveries and
	// re-attempts any that a transient error dropped. Running it BEFORE
	// the completion check closes the silent-video-loss window: a failed
	// spawn gets its checklist row inserted before the fixture can be
	// judged complete. See audit-2026-07-26 P1 #3.
	if awaiting, err := a.EventRepo.EventsAwaitingDiscovery(ctx, f.ID); err != nil {
		out.Errors = append(out.Errors, fmt.Sprintf("awaiting-discovery: %v", err))
	} else {
		for _, ev := range awaiting {
			if err := a.registerAndSpawnEvent(ctx, ev, ev, f.ID); err != nil {
				out.Errors = append(out.Errors, fmt.Sprintf("recover event=%s: %v", ev.ID, err))
			}
		}
	}

	// Step 6: fixture completion check. See
	// docs/design/proposals/completion-contract.md. Runs at the end so
	// any absence votes above that just soft-removed events count toward
	// "all events settled." Failure to check is non-fatal — the next
	// cycle will retry.
	ready, err := a.FixtureRepo.FixtureReadyToComplete(ctx, f.ID)
	if err != nil {
		out.Errors = append(out.Errors, fmt.Sprintf("completion check: %v", err))
		return out, nil
	}
	if !ready {
		return out, nil
	}
	if err := f.Complete(now); err != nil {
		// Domain invariant violation — log and continue. Should not
		// happen in practice since the completion check gates on state
		// being active.
		out.Errors = append(out.Errors, fmt.Sprintf("complete transition: %v", err))
		return out, nil
	}
	if err := a.FixtureRepo.Upsert(ctx, f); err != nil {
		out.Errors = append(out.Errors, fmt.Sprintf("upsert completed fixture: %v", err))
		return out, nil
	}
	out.Completed = true
	out.Structural = true
	a.emitFixtureCompleted(ctx, f.ID, now)
	return out, nil
}

// ── Composer emission helpers ──────────────────────────────────
//
// All emissions are nil-safe: if Composer is nil (test fixtures that
// don't wire it) the calls no-op. Publish failures log at WARN and
// continue — pg event_log is the source of truth and the outbox
// catch-up worker (future) republishes NATS gaps. See 2026-07-16
// Option B on composer-failure handling.

func (a *Activities) emitEventDetected(ctx context.Context, evID uuid.UUID, fixtureID int64, e *event.Event) {
	if a.Composer == nil {
		return
	}
	payload := eventinfra.EventDetectedPayload{
		EventID:    evID,
		FixtureID:  fixtureID,
		EventType:  string(e.Type),
		Detail:     string(e.Detail),
		Minute:     e.Minute,
		Extra:      e.Extra,
		PlayerName: playerName(e.Player),
		TeamID:     int64(e.Team.ID),
		TeamName:   e.Team.Name,
		Counter:    1,
	}
	if _, err := a.Composer.Publish(ctx, eventinfra.KindEventDetected, evID, fixtureID, payload); err != nil {
		// Non-fatal per Option B — log + continue.
		_ = err
	}
}

func (a *Activities) emitEventStable(ctx context.Context, evID uuid.UUID, fixtureID int64, e *event.Event) {
	if a.Composer == nil {
		return
	}
	payload := eventinfra.EventStablePayload{
		EventID:    evID,
		FixtureID:  fixtureID,
		EventType:  string(e.Type),
		Detail:     string(e.Detail),
		Minute:     e.Minute,
		Extra:      e.Extra,
		PlayerName: playerName(e.Player),
		TeamID:     int64(e.Team.ID),
		TeamName:   e.Team.Name,
	}
	if _, err := a.Composer.Publish(ctx, eventinfra.KindEventStable, evID, fixtureID, payload); err != nil {
		_ = err
	}
}

func (a *Activities) emitFixtureActivated(ctx context.Context, fixtureID int64, activatedAt time.Time, reason string) {
	if a.Composer == nil {
		return
	}
	payload := eventinfra.FixtureActivatedPayload{
		FixtureID:   fixtureID,
		ActivatedAt: activatedAt,
		Reason:      reason,
	}
	if _, err := a.Composer.Publish(ctx, eventinfra.KindFixtureActivated, uuid.Nil, fixtureID, payload); err != nil {
		_ = err
	}
}

func (a *Activities) emitEventRemoved(ctx context.Context, evID uuid.UUID, fixtureID int64, removedAt time.Time) {
	if a.Composer == nil {
		return
	}
	payload := eventinfra.EventRemovedPayload{
		EventID:   evID,
		FixtureID: fixtureID,
		RemovedAt: removedAt,
		Reason:    "debounce_zero",
	}
	if _, err := a.Composer.Publish(ctx, eventinfra.KindEventRemoved, evID, fixtureID, payload); err != nil {
		_ = err
	}
}

func (a *Activities) emitFixtureCompleted(ctx context.Context, fixtureID int64, completedAt time.Time) {
	if a.Composer == nil {
		return
	}
	payload := eventinfra.FixtureCompletedPayload{
		FixtureID:   fixtureID,
		CompletedAt: completedAt,
	}
	if _, err := a.Composer.Publish(ctx, eventinfra.KindFixtureCompleted, uuid.Nil, fixtureID, payload); err != nil {
		_ = err
	}
}

// playerName returns the Player's name or empty string if unknown.
// Payloads prefer empty string over a nullable field for JSON
// simplicity; downstream code gates on Player.Known() when needed.
func playerName(p event.Player) string {
	if p.Name == nil {
		return ""
	}
	return *p.Name
}

// registerAndSpawnEvent is the atomic register-on-flip step from
// the 2026-07-16 spawn rule. Both operations are idempotent (INSERT ON
// CONFLICT DO NOTHING for the row; failed-only deterministic identity for the
// spawn), so retry-after-partial-crash is safe. The spawner also owns FF-025's
// conservative stale-running recovery. Nil-safe: no-op if either EventRepo or
// Spawner is missing.
func (a *Activities) registerAndSpawnEvent(ctx context.Context, existing *event.Event, domainEv *event.Event, fixtureID int64) error {
	if a.EventRepo == nil || a.Spawner == nil {
		return nil
	}
	// Never spawn a search for an unknown player — there's no player token to
	// build a Twitter query from (Player.Known() contract). Placeholders are
	// pinned at debounce 0 so they never reach here via the trigger flip, but
	// the recovery pass also calls this, so guard explicitly.
	if !domainEv.Player.Known() {
		return nil
	}
	workflowID := fmt.Sprintf("event-%s", existing.ID)

	// Row insert first — must exist before the spawn returns so the
	// completion check in the same/next cycle sees "downstream pending."
	if err := a.EventRepo.RegisterDownstreamWorkflow(ctx, existing.ID, "discovery", workflowID); err != nil {
		// Skip the spawn to avoid an untracked workflow. The caller records
		// the error without failing reconciliation; next cycle re-attempts.
		return fmt.Errorf("register downstream: %w", err)
	}

	in := discoveryactivity.EventWorkflowInput{
		EventID:    existing.ID,
		FixtureID:  fixtureID,
		PlayerName: playerName(domainEv.Player),
		TeamName:   domainEv.Team.Name,
		TeamID:     int64(domainEv.Team.ID),
		Minute:     domainEv.Minute,
		Extra:      domainEv.Extra,
	}
	if err := a.Spawner.SpawnEvent(ctx, workflowID, in); err != nil {
		// The pending row exists. Surface the failure in Reconcile output;
		// the next recovery pass retries without completing the checklist.
		return fmt.Errorf("spawn downstream: %w", err)
	}
	return nil
}

// buildDomainEvent constructs an event.Event from the API payload
// with the caller-supplied seq. Seq is assigned by the reconcile
// identity matcher, which reuses active stored sequences and allocates above
// the complete active + removed history.
func (a *Activities) buildDomainEvent(f *fixture.Fixture, apiEv apifootball.APIFixtureEvent, domainType event.Type, seq int, now time.Time) (*event.Event, string, error) {
	teamID := apiEv.Team.ID
	teamName := apiEv.Team.Name
	if teamID == 0 {
		return nil, "", errors.New("apiEv missing team_id")
	}
	player := event.Player{ID: apiEv.Player.ID, Name: apiEv.Player.Name}

	minute := apiEv.Time.Elapsed
	var extra *int
	if apiEv.Time.Extra != nil {
		extra = apiEv.Time.Extra
	}
	// Empty detail fallback — vendor sometimes omits detail on early
	// event updates. Fall back to the domain type's string rep so the
	// row still has something searchable.
	detail := apiEv.Detail
	if detail == "" {
		detail = apifootball.APIEventDetail(string(domainType))
	}
	e := event.New(
		f.ID,
		event.Team{ID: teamID, Name: teamName},
		player,
		domainType,
		detail,
		minute,
		extra,
		seq,
		now,
	)
	// Assist is non-identity metadata (not part of NaturalKey), so it's set
	// after construction. nil/nil when the vendor reports no assister.
	e.Assist = event.Player{ID: apiEv.Assist.ID, Name: apiEv.Assist.Name}
	return e, e.NaturalKey, nil
}

// trackableType — thin wrapper around event.TrackableEventType that
// handles the nullable Comments field. Filtering logic lives in the
// domain layer per event_config.py's role in Python.
func trackableType(apiEv apifootball.APIFixtureEvent) event.Type {
	comments := ""
	if apiEv.Comments != nil {
		comments = *apiEv.Comments
	}
	t, _ := event.TrackableEventType(apiEv.Type, apiEv.Detail, comments)
	return t
}
