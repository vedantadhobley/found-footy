# MonitorWorkflow — design proposal (O2) — SUPERSEDED

**Status:** SUPERSEDED. Phase O2 (a/b/c) shipped 2026-07-08. The
implementation deviated from this proposal in several places based
on downstream design conversation. Do NOT read this doc as
authoritative — use these instead:

- `docs/orchestration.md` — as-shipped ledger of workflow
  + activity behavior
- `docs/decisions.md`:
  - 2026-07-07 APIStatus bucketing (SUSP/INT/PST=active,
    superseding the "add a postponed state" section here)
  - 2026-07-07 symmetric-counter debounce (superseding the
    delete-drops-on-presence proposal below)
  - 2026-07-08 test corpus + activity clock injection
    (superseding the "no scenario testing" gap)
  - 2026-07-09 don't vote absence during paused-play (extends
    the SUSP/INT/PST decision to event-level absence handling)

Sections of this proposal that were REJECTED during implementation:
- **Adaptive staging poll frequency tiering** (4h/24h/24h+ tiers) —
  rejected in favor of keeping Python's 15-min bucket amortization
  as-is.
- **New `postponed` fixture state** — rejected in favor of keeping
  PST fixtures in `active` state (SUSP/INT/PST=active decision).
- **Adaptive debounce thresholds** — deferred to Phase M with
  telemetry.

Kept from this proposal (accurately describes shipped state):
- Fire-per-cycle model (30s Temporal Schedule, SKIP overlap)
- Debounce via symmetric counter (though the model changed from
  what's described here — see decisions.md 2026-07-07 symmetric
  counter entry for the actual implementation)
- NATS emissions deferred to O3
- Fixture completion transition deferred to O3

Original preamble follows for historical context.

---

**Status (original):** design-first draft. Do not implement anything
from this doc until it's reviewed + signed off. Once signed off, the
ledger (`docs/orchestration.md`) is updated per the same-
commit discipline and this proposal is superseded.

**Cross-refs:**
- Plan intent — [`../../rebuild-plan.md`](../rebuild-plan.md) §5 W2
- Prior decisions —
  [`../../decisions.md`](../../decisions.md) 2026-07-07 fixture-activation +
  workflow-rename entries
- Working discipline —
  [`../../../AGENTS.md § Working discipline`](../../../AGENTS.md#working-discipline-mandatory-since-2026-07-07-retro)

## Purpose

Every 30 seconds, keep our Postgres model of active football matches
in sync with reality:

- **Staging fixtures**: on 15-min bucket boundaries, poll API for
  status changes (postponements, kickoff moves, live-status flip).
  Between boundaries, don't touch them (per the amortization design).
- **Active fixtures**: batch-fetch API state every cycle. Diff
  against pg events; register debounces; spawn Discovery for stable
  events; mark VAR removals.
- **Completed fixtures**: NOT this workflow's job in O2 (see
  "Deferred to O3" below).

## What's decided going in

From prior decisions.md entries and the follow-up conversation this
session:

| Decision | Source |
|---|---|
| Fire-per-cycle model — Temporal Schedule every 30s, `SCHEDULE_OVERLAP_POLICY_SKIP` | Plan §5 W2 + O1 pattern |
| 15-min bucket amortization for staging polling (`hour*4 + minute//15`) | 2026-07-07 staging-poll entry |
| 3-poll monitor-workflow debounce → `monitor_complete = TRUE` | Plan §5 W2 + schema `event_monitor_workflows` |
| 3-consecutive-poll drop debounce → mark removed | Plan §5 W2 + schema `event_drop_workflows` |
| Delete drops on presence (reset) — preserves Python's mechanic | Python `archive/src/data/events.py:222` `clear_drop_workflows` |
| Soft-delete on removal (mark `removed=TRUE`, keep row) | Plan §3 schema `events.removed BOOLEAN` — improves over Python's hard delete |
| Terminal state — once `removed=TRUE`, no un-remove | This session (avoids retriggering; matches your "3 in a row" bar) |
| Concurrent per-fixture processing via `workflow.Go` | This session (your concurrency-where-safe direction) |
| NATS emissions (`event.stable` etc.) — DEFERRED to O3 | This session — composer lands with its DiscoveryWorkflow consumer |
| Fixture completion path — DEFERRED to O3 | This session — "fully done" needs Discovery to define it |

## Prerequisites (must exist before workflow code)

Ordered by dependency:

**a. `internal/infra/pg/event_repo.go`** — implement `event.Repo`.
Schema tables already exist:
- `events` — CRUD
- `event_monitor_workflows` — INSERT ON CONFLICT DO NOTHING for
  debounce registration; COUNT for stability check
- `event_drop_workflows` — INSERT for absence; DELETE on presence
  reset; COUNT for removal threshold
- (event_download_workflows lands in O4)

**b. `event.Repo` interface additions** (three methods):
- `ClearDropWorkflows(ctx, eventID) error` — delete-on-presence reset
- `MarkRemoved(ctx, eventID, reason RemovalReason) error` — soft-delete
- `FlagMonitorComplete(ctx, eventID) (flipped bool, err error)` — sets
  `monitor_complete=TRUE`, returns true if this was the first flip

**c. `fixture.Repo` interface additions** (two methods):
- `ListActiveIDs(ctx) ([]int64, error)` — cheap ID list for batch fetch.
  (`ListByState(StateActive)` exists but returns whole rows — wasteful.)
- `ListStagingForBucketPoll(ctx, currentBucket int) ([]*Fixture, error)`
  — staging fixtures whose `LastPolledAt` bucket differs from current.
  Called only on boundary cycles.

**d. Verify `apifootball.ListFixturesByIDs` returns events per fixture.**
Live API call to `/fixtures?ids=<current-live-fixture>` and check the
response includes `events: [...]`. If yes, `APIFixture.Events` comment
gets corrected in fixtures.go. If no, add a `GetFixture(id)` method
that hits `/fixtures?id=<id>&events=true` per fixture (fallback).

**e. `apifootball.Client.ListLiveFixtures()` (optional)** — the
`/fixtures?live=all` endpoint returns all live fixtures + events in
one call, without needing to know IDs. Could be simpler than
"list ID from pg → fetch by ID" — but doesn't tell us WHICH of our
tracked fixtures are live. Keep the by-IDs path for now (couples
"our state" to "API state" cleanly); consider live=all later if
by-IDs proves expensive.

## Sequenced tasks (O2/a through O2/d)

Each sub-commit follows the working-discipline: read plan §, code +
docs/rebuild ledger update + decisions.md entry if any divergence,
verify diff before push.

### O2/a — Prerequisites

- `internal/infra/pg/event_repo.go` + tests (testcontainer)
- `fixture.Repo` additions + pg impl + tests
- `event.Repo` interface additions + pg impl + tests
- Verify `apifootball.ListFixturesByIDs` returns events (1 API call)
- Update `apifootball/fixtures.go` `APIFixture.Events` comment + docs
- Doc update: `docs/architecture.md` — mark event repo as
  shipped

Rough size: ~800 lines including tests. One commit.

### O2/b — Activities

Per plan §5 W2, with the improvements below. All live in
`internal/activity/monitor/`:

- `PreActivateUpcoming(lookahead time.Duration) → ActivateOutput`
  — DB-only; scans staging fixtures within lookahead. Runs every cycle.
- `PollStagingBucket(currentBucket int) → StagingPollOutput` — the
  amortized staging refresh. Runs only on boundary cycles (see workflow
  logic). Fetches API for staging fixtures whose LastPolledAt bucket
  differs; upserts state changes; handles PST → new `postponed` state
  (see "Postponed handling" section below).
- `ListActiveFixtureIDs() → []int64` — thin wrapper over
  `fixture.Repo.ListActiveIDs`.
- `FetchLiveFixtures(ids []int64) → []APIFixture` — chunks the batch
  by 20 (adapter's cap) and merges. Reuses `apifootball.ListFixturesByIDs`.
- `ReconcileFixtureAndEvents(APIFixture) → ReconcileOutput` — per fixture:
  refresh fixture row (state, elapsed, extra, scores, LastPolledAt);
  compare API events against pg events; for each API event, register
  monitor debounce + clear drop registry; for each pg event NOT in API,
  register drop debounce.
- `FlagStableEvents(fixtureID) → []StableEvent` — for each event where
  `RegisterMonitorWorkflow` count hit 3 this cycle (i.e. FlagMonitorComplete
  returned flipped=true), emit locally (log only in O2; NATS in O3).
- `MarkRemovedEvents(fixtureID) → []RemovedEvent` — for each event
  where drop_count >= 3, call `MarkRemoved(reason=VAR)` and log.

Rough size: ~1200 lines including tests + fakes. Split as O2/b1 (first
half) and O2/b2 (second half) if it feels large.

### O2/c — Workflow coordinator

- `internal/workflow/monitor.go` — MonitorWorkflow definition. Sequential
  outer loop (staging → active) but per-fixture concurrent execution via
  `workflow.Go` + `workflow.NewChannel` for aggregation.
- `internal/workflow/monitor_test.go` — WorkflowTestSuite tests covering
  the coordinator logic (activity call order, per-fixture concurrency,
  staging-bucket branch).

Rough size: ~600 lines.

### O2/d — Wire-up + live verification

- `cmd/worker/main.go` — register MonitorWorkflow + monitor activities;
  add `ensureMonitorSchedule` (mirrors `ensureIngestSchedule`,
  cron pattern `*/30 * * * * *`).
- Doc updates: `orchestration.md`, `deployment.md`, `temporal.md`.
- Live verification: today's games are live now (16 fixtures per the
  test I just ran). Trigger the workflow manually first via a
  `scripts/trigger_monitor` sibling; then let the schedule take over
  and watch for real event detection.

Rough size: ~200 lines + verification session.

## Logic improvements over Python

Beyond the delete-on-presence + soft-delete already covered:

<a id="1-adaptive-staging-tiering"></a>

### 1. Adaptive staging poll frequency

Python: on every 15-min boundary, poll ALL staging fixtures whose bucket
doesn't match current.

Problem: staging fixtures for next week get polled just as often as
fixtures kicking off in 2 hours. Real-world postponement risk peaks near
kickoff.

Improvement: tier the polling:

| Kickoff proximity | Poll frequency |
|---|---|
| Within 4h | Every 15-min bucket (current Python behavior) |
| 4h–24h | Every 60-min bucket (hour boundary only) |
| 24h+ | Once daily via IngestWorkflow (not by Monitor at all) |

The `PollStagingBucket` activity implements the tiering by filtering
`ListStagingForBucketPoll` per-tier. Expected outcome: ~90% reduction
in staging API calls during quiet periods, no cost to responsiveness
for near-kickoff fixtures.

**Question for you:** OK with this? Adds a small amount of logic to
the staging query. Or keep the flat 15-min bucket for now, add tiering
as an optimization if API burn becomes a problem?

### 2. Postponed fixture handling — new state

Python's `PST` handling was a hotfix. Domain currently has three states:
staging / active / completed. `PST` doesn't cleanly fit any.

Proposal: add a fourth state `postponed`. Rules:
- A fixture in `active` state whose API returns `status=PST` transitions
  to `postponed` (kickoff kept as the LAST-KNOWN kickoff; new kickoff
  unknown per API).
- A fixture in `staging` state whose API returns `status=PST` also
  transitions to `postponed` (same reason).
- `postponed` fixtures are NOT polled by Monitor (they're waiting for
  daily ingest to detect the rescheduled kickoff).
- Daily Ingest checks postponed fixtures — if API returns `status=NS`
  with a new kickoff, transition back to `staging`. If `status=CANC`,
  transition to `completed` (removed_reason logic on any active events).
- If postponed >30 days, `PruneOldFixtures` removes them.

**Question for you:** OK with adding `postponed` as a state? Schema
change: `ALTER TYPE fixture_state ADD VALUE 'postponed';`. Domain
changes: new methods `Postpone(at time.Time, reason string)` and
`Resume(newKickoff, at time.Time)` on `Fixture`.

### 3. Adaptive debounce thresholds (deferred)

Idea: reduce debounce from 3 to 2 for late-game goals (min > 85) to
avoid missing the last 90 seconds of a match. Real impact — 92-minute
goals are common; a 90-second debounce means we spawn Discovery after
the final whistle.

**But**: 2-poll debounce is more susceptible to false positives (a
briefly-mis-reported event that vanishes on the next poll). Trade-off
matters. Would want telemetry on how often this happens before
committing.

**Recommendation:** DEFER to Phase M or wherever we start tuning based
on real data. Ship O2 with the flat 3-poll threshold.

### 4. Event-log semantic emissions — deferred to O3

Plan §11 pillar 4 says every state transition (event.detected,
event.stable, event.removed, fixture.activated, fixture.completed)
emits a semantic event to NATS + `event_log`. In O2 we'll:

- WRITE to `event_log` on every transition (durable audit — cheap,
  no consumer coupling)
- SKIP NATS emissions (they need the `internal/infra/event/` composer
  which is stubbed)

DiscoveryWorkflow spawn in O3 will drive the NATS wire-up + subscribers.

## Deferred to O3

- Fixture completion logic (needs Discovery/Video to define "fully done")
- NATS emissions (`event.detected`, `event.stable`, `event.removed`)
- DiscoveryWorkflow spawn on `event.stable`
- The `internal/infra/event/` composer

## Test strategy

Two layers:

**Unit / integration (fast, offline):**
- Activity unit tests with fake `event.Repo` + `fixture.Repo`.
- Workflow tests via `testsuite.WorkflowTestSuite`.
- pg integration tests via testcontainers-go.

**Live verification (uses today's games):**
- `scripts/trigger_monitor/main.go` — dev-only manual trigger.
- Given 16 fixtures are live right now, activate a subset via
  ingest → run monitor → watch pg populate events + debounce tables.
- Real end-to-end proof that:
  - Active fixtures are polled correctly
  - Events are detected + debounced
  - Late-appearing events increment monitor count
  - Missing events increment drop count
  - Delete-on-presence resets drop count on flicker

**Mock-fixture testing for edge cases** (your suggestion):
- A `scripts/mock_fixture/main.go` that inserts a fake fixture with a
  configurable kickoff and status. Useful for testing pre-activation,
  imminent-activation, postponed transition without waiting for a real
  match to hit those states.
- Not blocking O2 shipping; add if the live-game testing surfaces gaps.

## Open questions for your review

Before I touch code:

1. **Postponed state** — OK to add `postponed` as a fourth `fixture.State`?
2. **Adaptive staging tiering** — ship with tiered (4h/24h/24h+) OR
   flat 15-min bucket for O2, tier later?
3. **Split O2/b activity commit** — one commit for all 6 activities, or
   split into O2/b1 (fixture/event reconciliation) + O2/b2 (debounce/spawn/
   removal)?
4. **Anything I've misread** about your intent for Monitor?

## Change log

- 2026-07-07 — initial draft (this doc). Awaiting review.
