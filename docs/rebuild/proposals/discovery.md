# DiscoveryWorkflow + NATS composer — design proposal (O3)

**Status:** design-first draft. Do not implement anything from this
doc until it's reviewed + signed off.

**Cross-refs:**
- Plan intent — [`../../rebuild-plan.md`](../../rebuild-plan.md) §5 W3, §8 SSE + NATS
- Prior decisions — [`../../decisions.md`](../../decisions.md):
  - 2026-07-16 Downstream workflow spawn via Temporal-direct + register-on-flip (this proposal's O3/b + O3/c now conform to it)
  - 2026-07-11 Fixture completion contract via pluggable per-event workflow checklist
  - 2026-07-01 Workspace NATS as event bus (scope reduced by 2026-07-16 to external fan-out only)
  - 2026-07-02 NATS metadata-plane; video bytes via HTTP
  - 2026-07-07 Workflow renames (TwitterWorkflow → DiscoveryWorkflow)
  - 2026-07-07 Symmetric-counter debounce (events fed via
    downstream_triggered / hitZero)
- Working discipline — [`../../../AGENTS.md § Working discipline`](../../../AGENTS.md#working-discipline-mandatory-since-2026-07-07-retro)

## Purpose

Bridge Monitor's per-event triggers to the video pipeline. When
Monitor's `RegisterEventPresence` returns `justTriggered=true`,
downstream work MUST happen exactly once per event: search Twitter,
download candidates, validate, dedup, publish.

O3 covers ONLY the trigger + fan-out. The actual video work
(download, validation, persistence, dedup) is O4/O5.

## Semantic model (from decisions.md)

Two orthogonal per-event outcomes inside Monitor's `ReconcileFixture`
cycle:
- **Event stabilized** (`justTriggered=true`) — the flag flip.
  Monitor's activity flips `events.downstream_triggered=true`, inserts
  the Discovery row into `event_downstream_workflows`, and spawns
  DiscoveryWorkflow via the Temporal client — all in the same activity
  ([2026-07-16 decision](../../decisions.md)). In the same activity,
  `event.stable` is also published to NATS **for external consumers
  only** (SSE bridge, webhook delivery). The NATS emit is not the
  trigger path for Discovery.
- **Event removed** (`hitZero=true`) — the soft-delete. Monitor
  publishes `event.removed` to NATS for external fan-out, and
  (deferred to O4/O5) triggers the destroy pipeline: cancel in-flight
  DiscoveryWorkflow / downstream, mark video_shares as removed.

Plus fixture-level:
- **`fixture.activated`** — staging → active (Ingest or Monitor
  activation). Emitted to NATS for external fan-out.
- **`fixture.completed`** — active → completed. Emitted when
  `FixtureReadyToComplete` gates true and Monitor commits the state
  transition ([2026-07-11 completion contract](../../decisions.md)).

## What's decided going in

| Decision | Source |
|---|---|
| NATS pub is metadata-plane only (event names + payload). Video bytes never go over NATS — HTTP direct to Garage. | 2026-07-02 |
| Dual-write pattern: pg `event_log` (audit) + NATS publish (external fan-out only, not workflow triggers). Composer at `internal/infra/event/` handles both. | Plan §11 pillar 4, narrowed by 2026-07-16 |
| Discovery spawned via Temporal client from Monitor's `ReconcileFixture` activity — same activity as flag flip + `event_downstream_workflows` row insert. NATS emits `event.stable` alongside, for external consumers only. Rationale: eliminates the flag-flip-vs-row-insert race; keeps observable Temporal workflow graph; NATS as broker earns its keep only across process/project/node boundaries. | 2026-07-16 |
| RejectDuplicate at deterministic workflow-ID `discovery-{event_id}` — server-side idempotency; activity retry after crash between insert and spawn returns `WorkflowExecutionAlreadyStarted`, swallowed as success. | 2026-07-16 |
| 10-attempt Twitter search with 1-min spacing — kept from Python (user's explicit call, 2026-07-08 conversation) | User |
| Video-URL sharing across events — kept from Python + LSH bucketing for cross-corpus dedup — extended to two-layer model (URL check → content hash → perceptual hash) | User + earlier proposal |

## Sequenced sub-commits

Each smaller than O2's sub-commits since Monitor's shape is already
established. Estimates: total ~1.5-2 sessions.

### O3/a — NATS event composer (prerequisites)

`internal/infra/event/composer.go` — dual-write helper:
- `Publish(ctx, kind, payload)` — INSERT INTO event_log + JetStream publish, best-effort. Skew is a metric, not a failure.
- Kinds: `fixture.activated`, `fixture.completed`, `event.detected`, `event.stable`, `event.removed`, `event.rank_recalculated`
- Payload types per kind — small structs, JSON-serialized

Testing: pg testcontainer + NATS testcontainer, verify both writes happen + skew metric increments on partial failure.

~400 lines.

### O3/b — Monitor emits events + spawns Discovery

Update `internal/activity/monitor/activities.go` `ReconcileFixture` to:
- Take an `EventComposer` dep (dual-write helper from O3/a) and a
  `DownstreamSpawner` dep — thin interface over the Temporal client
  so the activity stays unit-testable with a fake spawner.
- On new event insert: publish `event.detected` (external fan-out).
- On `justTriggered=true` — the flag flip step:
  1. Same pg tx as the flag flip: INSERT into
     `event_downstream_workflows` (event_id, workflow_type='discovery',
     workflow_id=`discovery-{event_id}`, started_at=NOW(),
     completed_at=NULL). `ON CONFLICT DO NOTHING` for Temporal replay
     idempotency.
  2. Call `DownstreamSpawner.SpawnDiscovery(ctx, DiscoveryInput{...},
     workflowID='discovery-{event_id}',
     WorkflowIDReusePolicy=RejectDuplicate)`. Swallow
     `WorkflowExecutionAlreadyStarted` — expected on activity retry
     after partial-success crash.
  3. Publish `event.stable` (external fan-out) via composer.
- On `hitZero=true`: publish `event.removed` (external fan-out).
  Destroy pipeline (workflow cancel + video_shares soft-delete)
  deferred to a later phase.

Update `internal/workflow/active_poll.go` — no signature change;
`ReconcileFixture` still returns `ReconcileOutput` with a `Completed`
bool. Spawn is inside the activity, not the workflow, so the workflow
stays deterministic-friendly.

Update `cmd/worker/main.go` to construct the composer + a Temporal-
client-backed `DownstreamSpawner` and wire both into `monitorActs`.

Update existing corpus scenarios — add
`expected_final_state.event_log` assertions to verify NATS-published
emissions, and `expected_final_state.event_downstream_workflows`
assertions to verify Monitor-inserted Discovery rows.

~400 lines (grew from ~300 in prior draft; the row-insert +
DownstreamSpawner wiring is the delta).

### O3/c — DiscoveryWorkflow skeleton

`internal/workflow/discovery.go` — MVP DiscoveryWorkflow:
- Input: `DiscoveryInput{EventID, FixtureID, PlayerName, TeamName, TeamID, Minute}`
- Body: log and return. No Twitter search yet — that's O3/d.
- On exit (success or failure): call an activity that marks the
  `event_downstream_workflows` row as `completed_at=NOW()`,
  `outcome_class` set accordingly. Row already exists — Monitor
  inserted it in O3/b, so this is UPDATE not INSERT.

Register `DiscoveryWorkflow` in `cmd/worker/main.go` alongside
`ActivePollWorkflow` and `StagingPollWorkflow`. No NATS subscriber
goroutine — Monitor spawns Discovery directly via the Temporal client
inside its activity (see O3/b + [2026-07-16 decision](../../decisions.md)).

Test: scenario that drives Monitor to trigger, then verifies —
(a) a Discovery workflow was scheduled with the deterministic ID
`discovery-{event_id}`,
(b) `event_downstream_workflows` has a row with `completed_at IS NULL`
during Discovery execution, and
(c) after Discovery completes, the row is marked `completed_at` +
`outcome_class`.

~250 lines (down from ~400 in the prior draft — no NATS subscriber
goroutine, no JetStream consumer wiring, no redelivery ack/drop
logic).

### O3/d — Actual Twitter search (this is where "downstream complexity" begins)

Deferred to a separate sub-phase because:
- Twitter search string RAG design is still open (user's explicit deferral)
- Twitter service in Go is not built (dev runs stub; prod is Python)
- Video download + validation belongs in O4

O3/d becomes the bridge: for now, Discovery just calls a stub activity that "would have done Twitter" and logs. Real Twitter integration lands with the Twitter service port + RAG redesign (own phase, TBD).

## Deferred to O4/O5

- Video download (syndication + FFmpeg)
- Video validation (Qwen3-VL vision)
- Perceptual + content hash dedup + LSH bucketing
- Asset persistence + share ranking
- Destroy pipeline (Temporal cancel + video_shares soft-delete on
  `event.removed`)

## Open questions for your review

**Discovery-trigger transport is closed** (2026-07-16 decision:
Temporal-direct + register-on-flip). The four questions below remain.

1. **NATS composer scope for O3/a — full dual-write or NATS-only for now?**
   Full dual-write is right per the plan but adds pg schema pressure (event_log table + dedup constraints). NATS-only is faster to ship. My lean: **full dual-write** — the audit trail matters, and building it now avoids retrofitting later.

2. **Should MonitorWorkflow update its scenario assertions in O3/b?**
   If yes, every existing debounce scenario adds `expected_final_state.event_log` blocks to verify emissions **and** `expected_final_state.event_downstream_workflows` blocks to verify Monitor-inserted Discovery rows. Real coverage but more YAML noise. My lean: **yes** — the harness is exactly the place to verify emissions + row-insert atomicity, and doing it now catches spawn-path bugs at commit time.

3. **Twitter service — do we port Playwright-Go now, or keep the Go stub through O3?**
   Real question because a Discovery workflow that "logs and returns" isn't testable end-to-end. But porting Twitter is a whole separate track. My lean: **stub for O3/c, port in a dedicated Twitter-service commit later**. Discovery's control flow is verifiable in scenarios without real Twitter.

4. **Video URL sharing (from Python) — preserve or design fresh?**
   Python's URL-sharing across events (multiple events sharing a video_asset via video_shares) worked for the trivial case. My earlier proposal was a 3-layer dedup (URL → content hash → perceptual hash + LSH). That belongs in O4/O5, but the SCHEMA (video_assets.content_hash UNIQUE, perceptual_hash_prefix indexed) already exists in schema.sql. My lean: **keep the schema, defer design conversation to O4**.

Sign off on the 4 questions above and O3/a starts.
