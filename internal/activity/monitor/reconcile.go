// Active-fixture fetch and per-fixture reconciliation activities.
package monitor

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/domain/event"
	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

// ListActiveFixtureIDsOutput carries the active fixture IDs.
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
//  2. Diff API events against pg events (including removed sequence history).
//  3. For each API event that doesn't match an active pg event: Insert a fresh
//     generation (seeds debounce_count=1 + records this workflow's presence
//     vote). Removed history reserves old sequences but cannot suppress current
//     provider evidence.
//  4. For each pg event ALSO in API: RegisterEventPresence
//     (increments count, may flip downstream_triggered).
//  5. For each pg event NOT in API: hold a goal when the aggregate score
//     proves the response omitted one; otherwise RegisterEventAbsence
//     (decrements count, may hit zero + soft-delete).
//
// Removed event generations are NOT voted against or revived. Their sequences
// remain reserved; equivalent provider evidence starts a new generation with a
// fresh natural key and UUID.
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
	prevTerminalObservedAt := f.TerminalObservedAt
	prevElapsed, prevExtra := f.APIElapsed, f.APIExtra
	prevHomeScore, prevAwayScore := f.HomeScore, f.AwayScore
	prevHomeWinner, prevAwayWinner := f.HomeWinner, f.AwayWinner
	prevHomePen, prevAwayPen := f.HomePenalty, f.AwayPenalty
	// Score and event-array facts must come from the same provider response.
	// The immutable view both guards destructive goal-absence votes below and
	// records provider parity if this cycle completes the fixture.
	scoreInventory := newScoreEventInventory(in.APIFixture)
	providerScoreEventParity := scoreInventory.terminalProviderScoreEventParity(
		in.APIFixture.Fixture.Status.Short,
		in.APIFixture.Teams.Home.ID,
		in.APIFixture.Teams.Away.ID,
	)

	f.UpdateFromPoll(
		fixture.APIStatus{Short: in.APIFixture.Fixture.Status.Short, Long: in.APIFixture.Fixture.Status.Long},
		in.APIFixture.Fixture.Status.Elapsed,
		in.APIFixture.Fixture.Status.Extra,
		in.APIFixture.Goals.Home,
		in.APIFixture.Goals.Away,
		now,
	)
	// Mirror the nullable shootout before deriving result state: normal/AET
	// winner state comes from the aggregate score, PEN comes from this shootout,
	// and exceptional outcomes retain the provider's explicit flags. Result
	// display data does not control terminal-grace eligibility.
	f.UpdatePenalty(in.APIFixture.Score.Penalty.Home, in.APIFixture.Score.Penalty.Away)
	f.UpdateResult(in.APIFixture.Teams.Home.Winner, in.APIFixture.Teams.Away.Winner)

	// N4: classify the non-event changes now (event-driven structural signals +
	// completion set out.Structural incrementally below). ClockChanged/Minute/
	// Extra are set here so every return path carries them.
	out.Minute = derefInt(f.APIElapsed)
	out.Extra = f.APIExtra
	out.ClockChanged = intPtrChanged(prevElapsed, f.APIElapsed) || intPtrChanged(prevExtra, f.APIExtra)
	if prevStatus != f.APIStatus.Short || timePtrChanged(prevTerminalObservedAt, f.TerminalObservedAt) ||
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
	// immutable sequence tombstones that prevent brace renumbering and key reuse
	// without suppressing a later reappearance generation.
	allEvents, err := a.EventRepo.ListAllByFixture(ctx, f.ID)
	if err != nil {
		return out, fmt.Errorf("monitor.ReconcileFixture: list fixture event history: %w", err)
	}

	pgByKey := make(map[string]*event.Event, len(allEvents))
	for _, stored := range allEvents {
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
		} else {
			// New event — Insert seeds debounce_count=1 when the player is
			// known, or 0 (placeholder, no vote) when the player is unknown.
			// The identity allocator never reuses a removed sequence, so an
			// exact post-removal reappearance reaches this path with a new
			// natural key and UUID rather than being swallowed by its tombstone.
			if err := a.EventRepo.Insert(ctx, domainEv, in.WorkflowID); err != nil {
				out.Errors = append(out.Errors, fmt.Sprintf("insert event=%s: %v", key, err))
				continue
			}
			out.NewEventsDetected++
			out.Structural = true

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

	// Step 6: fixture completion check. See the FF-063 terminal-grace decision.
	// Runs at the end so
	// any absence votes above that just soft-removed events count toward
	// "all events settled." Failure to check is non-fatal — the next
	// cycle will retry.
	assessment, err := a.FixtureRepo.AssessCompletion(
		ctx,
		f.ID,
		now.Add(-a.TerminalGracePeriod),
	)
	if err != nil {
		out.Errors = append(out.Errors, fmt.Sprintf("completion check: %v", err))
		return out, nil
	}
	if !assessment.Ready {
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
	a.emitFixtureCompleted(ctx, f, now, assessment, providerScoreEventParity)
	return out, nil
}
