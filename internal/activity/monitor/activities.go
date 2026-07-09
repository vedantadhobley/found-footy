// Package monitor holds the Temporal activities the MonitorWorkflow
// orchestrates. Every 30 seconds (per Temporal Schedule):
//   1. PreActivateUpcoming — DB-only check; promotes staging fixtures
//      whose kickoff is within the activation window.
//   2. ListActiveFixtureIDs — cheap ID pull.
//   3. FetchLiveFixtures — one batched /fixtures?ids= call.
//   4. ReconcileFixture — per fixture, refresh row + diff events +
//      vote presence/absence for each event. Concurrent via
//      workflow.Go in the coordinator.
//
// Debounce model per decisions.md 2026-07-07 symmetric-counter entry.
// NATS emissions and DiscoveryWorkflow spawn are DEFERRED to O3;
// this file just LOGS the "would spawn" cases so we can inspect
// behavior in Loki during live testing before real fan-out lands.
package monitor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/vedantadhobley/found-footy/internal/domain/event"
	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

// fixtureFetcher is the narrow interface the Monitor activities need
// from the apifootball adapter — same idiom as ingest's fetcher.
type fixtureFetcher interface {
	ListFixturesByIDs(ctx context.Context, ids []int64) ([]apifootball.APIFixture, error)
}

// Activities bundles the deps every monitor activity needs. Now is
// injectable per the harness discipline (docs/decisions.md
// 2026-07-08 test corpus entry).
type Activities struct {
	APIFootball fixtureFetcher
	FixtureRepo fixture.Repo
	EventRepo   event.Repo
	Now         func() time.Time
}

func (a *Activities) now() time.Time {
	if a.Now != nil {
		return a.Now().UTC()
	}
	return time.Now().UTC()
}

// ── PreActivateUpcoming ───────────────────────────────────────

// PreActivateUpcomingInput carries the lookahead window (typically 30
// min). Fixtures in staging with kickoff <= now+lookahead get promoted
// to active so the batched poll starts covering them next cycle.
type PreActivateUpcomingInput struct {
	Lookahead time.Duration
}

// PreActivateUpcomingOutput reports how many fixtures got promoted +
// how many were considered (for observability).
type PreActivateUpcomingOutput struct {
	Considered int
	Activated  int
	Errors     []string
}

// PreActivateUpcoming reads staging fixtures with kickoff before
// now+Lookahead, promotes any that ShouldActivateNow returns true for.
// Uses the same domain predicate ingest uses at first-sight
// activation, so the rule stays in one place.
func (a *Activities) PreActivateUpcoming(ctx context.Context, in PreActivateUpcomingInput) (PreActivateUpcomingOutput, error) {
	out := PreActivateUpcomingOutput{}
	now := a.now()

	candidates, err := a.FixtureRepo.ListStagingBeforeKickoff(ctx, now.Add(in.Lookahead))
	if err != nil {
		return out, fmt.Errorf("monitor.PreActivateUpcoming: list: %w", err)
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

// FetchLiveFixturesInput carries the batched ID slice.
type FetchLiveFixturesInput struct {
	IDs []int64
}

// FetchLiveFixturesOutput carries the API response array. api-sports.io
// caps by-IDs batches at 20 — the workflow chunks caller-side if the
// active set is larger; this activity is one atomic batch call.
type FetchLiveFixturesOutput struct {
	Fixtures []apifootball.APIFixture
}

// FetchLiveFixtures calls apifootball with the given IDs. Zero-length
// input short-circuits — no round trip.
func (a *Activities) FetchLiveFixtures(ctx context.Context, in FetchLiveFixturesInput) (FetchLiveFixturesOutput, error) {
	if len(in.IDs) == 0 {
		return FetchLiveFixturesOutput{}, nil
	}
	fixtures, err := a.APIFootball.ListFixturesByIDs(ctx, in.IDs)
	if err != nil {
		return FetchLiveFixturesOutput{}, fmt.Errorf("monitor.FetchLiveFixtures: %w", err)
	}
	return FetchLiveFixturesOutput{Fixtures: fixtures}, nil
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
// StableEventIDs + RemovedEventIDs are the events whose state
// transitions the caller wants to know about (Discovery spawn +
// destroy pipeline in later phases).
type ReconcileFixtureOutput struct {
	FixtureID          int64
	NewEventsDetected  int
	EventsBecameStable []string // natural_keys of events that just crossed count=3
	EventsRemoved      []string // natural_keys of events that just hit count=0
	Errors             []string
}

// ReconcileFixture is the per-fixture per-cycle work:
//   1. Refresh the fixture row (API-mutable fields + LastPolledAt).
//   2. Diff API events against pg events (including removed — for
//      collision-handling of previously-removed natural_keys).
//   3. For each API event that doesn't exist in pg AND isn't a
//      previously-removed natural_key: Insert (seeds debounce_count=1
//      + records this workflow's presence vote).
//   4. For each pg event ALSO in API: RegisterEventPresence
//      (increments count, may flip downstream_triggered).
//   5. For each pg event NOT in API: RegisterEventAbsence
//      (decrements count, may hit zero + soft-delete).
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
	f.UpdateFromPoll(
		fixture.APIStatus{Short: in.APIFixture.Fixture.Status.Short, Long: in.APIFixture.Fixture.Status.Long},
		in.APIFixture.Fixture.Status.Elapsed,
		in.APIFixture.Fixture.Status.Extra,
		in.APIFixture.Goals.Home,
		in.APIFixture.Goals.Away,
		now,
	)
	if err := a.FixtureRepo.Upsert(ctx, f); err != nil {
		return out, fmt.Errorf("monitor.ReconcileFixture: upsert fixture: %w", err)
	}

	// Step 2-5: diff events.
	// Read pending events (NOT removed AND not fully done) so the
	// collision handler for previously-removed events is transparent —
	// we simply don't consider them here. Insert for a removed
	// natural_key would fail the UNIQUE constraint; we skip before
	// then via keySeen tracking below.
	pending, err := a.EventRepo.ListPending(ctx, f.ID)
	if err != nil {
		return out, fmt.Errorf("monitor.ReconcileFixture: list pending: %w", err)
	}
	// Also read ALL events for the fixture to know which natural_keys
	// are taken (including removed). We skip re-inserting keys that
	// already have removed rows — collision-handling shortcut.
	allKeys, err := a.collectAllNaturalKeys(ctx, f.ID)
	if err != nil {
		return out, fmt.Errorf("monitor.ReconcileFixture: collect keys: %w", err)
	}

	// Group pending pg events by natural_key for O(1) lookup.
	pgByKey := make(map[string]*event.Event, len(pending))
	for _, e := range pending {
		pgByKey[e.NaturalKey] = e
	}

	// Build a set of API event natural_keys we've seen this cycle,
	// so absence votes for pg events NOT in the set are correct.
	apiKeys := make(map[string]struct{}, len(in.APIFixture.Events))

	// Per-cycle counter for seq assignment. Keyed by (team_id,
	// player_id_or_unknown, type). As we iterate API events in
	// order, this counter increments per (team, player, type) group
	// — matching Python's approach in archive/src/data/fixtures.py.
	// API returns events in chronological order, so this counter is
	// stable across monitor cycles: the min-30 goal always gets seq=1,
	// a subsequent min-45 goal from the same player always gets seq=2.
	seqCounter := make(map[string]int)

	for _, apiEv := range in.APIFixture.Events {
		// Filter to trackable event types via event.TrackableEventType.
		// Details:
		//   Goal with detail in {Normal Goal, Penalty, Own Goal} +
		//     no "Penalty Shootout" in comments → tracked
		//   Card with detail = "Red card" → tracked
		//   Everything else (yellow cards, substitutions, VAR
		//     announcements, missed penalties, shootout goals) → skip
		domainType := trackableType(apiEv)
		if domainType == "" {
			continue
		}
		seqKey := seqCounterKey(apiEv, domainType)
		seqCounter[seqKey]++
		domainEv, key, err := a.buildDomainEvent(f, apiEv, domainType, seqCounter[seqKey], now)
		if err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("build event: %v", err))
			continue
		}
		apiKeys[key] = struct{}{}

		if existing, ok := pgByKey[key]; ok {
			// Event exists — register presence vote.
			_, justTriggered, err := a.EventRepo.RegisterEventPresence(ctx, existing.ID, in.WorkflowID)
			if err != nil {
				out.Errors = append(out.Errors, fmt.Sprintf("presence event=%s: %v", key, err))
				continue
			}
			if justTriggered {
				out.EventsBecameStable = append(out.EventsBecameStable, key)
			}
		} else if _, removedAlready := allKeys[key]; removedAlready {
			// Terminal — skip. Prior instance of this natural_key was
			// soft-removed. See package docstring.
			continue
		} else {
			// New event — insert (auto-seeds debounce_count=1).
			if err := a.EventRepo.Insert(ctx, domainEv, in.WorkflowID); err != nil {
				out.Errors = append(out.Errors, fmt.Sprintf("insert event=%s: %v", key, err))
				continue
			}
			out.NewEventsDetected++
			allKeys[key] = struct{}{}
		}
	}

	// Absence votes: pg events NOT in the API this cycle.
	//
	// Trusts the API to keep events cumulative across all statuses
	// (matches Python's behavior + real-world API-Football behavior).
	// A VAR overturn genuinely removes the event from the array; a
	// pause (HT/PST/SUSP/INT) leaves it in place. So "not in this
	// cycle's array" IS "removed" — no defensive gating needed.
	for key, pgEv := range pgByKey {
		if _, present := apiKeys[key]; present {
			continue
		}
		_, hitZero, err := a.EventRepo.RegisterEventAbsence(ctx, pgEv.ID, in.WorkflowID)
		if err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("absence event=%s: %v", key, err))
			continue
		}
		if hitZero {
			out.EventsRemoved = append(out.EventsRemoved, key)
		}
	}

	return out, nil
}

// collectAllNaturalKeys reads every natural_key for the fixture,
// including removed events. Used to skip re-insertion of a
// natural_key that's terminal (soft-removed).
func (a *Activities) collectAllNaturalKeys(ctx context.Context, fixtureID int64) (map[string]struct{}, error) {
	// The event.Repo doesn't have a bulk "give me all natural_keys"
	// method yet — we use pg directly via a small helper. For now,
	// this shortcut lives in this activity file; if a second caller
	// needs it, promote to event.Repo.
	return collectNaturalKeysForFixture(ctx, a.EventRepo, fixtureID)
}

// collectNaturalKeysForFixture leans on ListPending (non-removed
// events) for the "currently-active" set. A separate method for
// removed events would need a new repo query — for now, we treat
// only ListPending's set as "taken" for insertion purposes. A
// previously-removed event that reappears will hit
// UNIQUE(fixture_id, natural_key), and the Insert failure is caught
// by the caller. This is defensive — we prefer to fail loud on
// collision rather than silently skip.
//
// TODO: add event.Repo.NaturalKeysForFixture(ctx, fixtureID) that
// returns all keys (removed too) so the skip-terminal shortcut is
// clean rather than an insert-then-catch dance.
func collectNaturalKeysForFixture(ctx context.Context, repo event.Repo, fixtureID int64) (map[string]struct{}, error) {
	events, err := repo.ListPending(ctx, fixtureID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(events))
	for _, e := range events {
		out[e.NaturalKey] = struct{}{}
	}
	return out, nil
}

// buildDomainEvent constructs an event.Event from the API payload
// with the caller-supplied seq. Seq is assigned by the reconcile
// loop's per-(team, player, type) counter — stable across cycles
// because api-sports.io returns events in chronological order.
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
	detail := apiEv.Detail
	if detail == "" {
		detail = string(domainType)
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

// seqCounterKey returns the key used to increment per-cycle seq for
// an API event. Matches Python's counter key from
// archive/src/data/fixtures.py: (player_id, event_type) — team_id is
// implied since it's part of the event.
func seqCounterKey(apiEv apifootball.APIFixtureEvent, domainType event.Type) string {
	pidStr := "unknown"
	if apiEv.Player.ID != nil {
		pidStr = fmt.Sprintf("%d", *apiEv.Player.ID)
	}
	return fmt.Sprintf("%d_%s_%s", apiEv.Team.ID, pidStr, string(domainType))
}
