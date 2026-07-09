# DiscoveryWorkflow + NATS composer — design proposal (O3)

**Status:** design-first draft. Do not implement anything from this
doc until it's reviewed + signed off.

**Cross-refs:**
- Plan intent — [`../../rebuild-plan.md`](../../rebuild-plan.md) §5 W3, §8 SSE + NATS
- Prior decisions — [`../../decisions.md`](../../decisions.md):
  - 2026-07-01 Workspace NATS as event bus
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

Two orthogonal signals from Monitor per cycle:
- **`event.stable`** — event just crossed to `downstream_triggered=true`.
  Discovery spawns exactly once.
- **`event.removed`** — event just hit `debounce_count=0` and was
  soft-deleted. Destroy pipeline runs: cancel any in-flight
  Discovery/VideoValidation workflows, mark video_shares as removed.

Plus fixture-level:
- **`fixture.activated`** — staging → active (Ingest or Monitor
  pre-activation)
- **`fixture.completed`** — active → completed (deferred: needs
  Discovery to define "fully done")

## What's decided going in

| Decision | Source |
|---|---|
| NATS pub is metadata-plane only (event names + payload). Video bytes never go over NATS — HTTP direct to Garage. | 2026-07-02 |
| Dual-write pattern: pg `event_log` (audit) + NATS publish (fan-out). Composer at `internal/infra/event/` handles both atomically. | Plan §11 pillar 4 |
| Discovery triggered via NATS subscriber goroutine in `cmd/worker`, not via child-workflow spawn from Monitor. Decoupling: Monitor doesn't know Discovery exists. | Plan §5 W2 discovery-trigger subsection |
| REJECT_DUPLICATE at workflow-ID level `discovery-{event_id}` — server-side idempotency, no double-spawn even if event.stable is redelivered. | Plan §5 W2 |
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

### O3/b — Monitor emits events

Update `internal/activity/monitor/activities.go` to:
- Take an `EventComposer` dep
- On new event insert: publish `event.detected`
- On `justTriggered=true`: publish `event.stable`
- On `hitZero=true`: publish `event.removed`

Update MonitorWorkflow reconcile output to include the payloads (not just log-only as O2 has today).

Update `cmd/worker/main.go` to wire the composer into `monitorActs`.

Update existing corpus scenarios — add `expected_final_state.event_log` assertions to verify emissions.

~300 lines.

### O3/c — DiscoveryWorkflow skeleton + NATS subscriber

`internal/workflow/discovery.go` — MVP DiscoveryWorkflow:
- Input: `DiscoveryInput{EventID, FixtureID, PlayerName, TeamName, TeamID, Minute}`
- Body: log and return. NO Twitter search yet — that's O3/d.

`cmd/worker/main.go` — subscriber goroutine reading NATS `event.stable`:
- Durable consumer via `nats_events` stream + `discovery_trigger` name
- On message: `client.ExecuteWorkflow(discovery, WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE, workflowID=discovery-{event_id})`
- ErrScheduleAlreadyRunning → ack + drop (expected on redelivery)
- ErrWorkflowExecutionAlreadyStarted → ack + drop (dedup working)

Test: scenario that drives Monitor to trigger, then verifies a Discovery workflow was scheduled.

~400 lines.

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

1. **NATS composer scope for O3/a — full dual-write or NATS-only for now?**
   Full dual-write is right per the plan but adds pg schema pressure (event_log table + dedup constraints). NATS-only is faster to ship. My lean: **full dual-write** — the audit trail matters, and building it now avoids retrofitting later.

2. **Should MonitorWorkflow update its scenario assertions in O3/b?**
   If yes, every existing debounce scenario adds `expected_final_state.event_log` blocks to verify emissions. Real coverage but more YAML noise. My lean: **yes** — the harness is exactly the place to verify emissions, and doing it now catches emission bugs at commit time.

3. **Twitter service — do we port Playwright-Go now, or keep the Go stub through O3?**
   Real question because a Discovery workflow that "logs and returns" isn't testable end-to-end. But porting Twitter is a whole separate track. My lean: **stub for O3/c, port in a dedicated Twitter-service commit later**. Discovery's control flow is verifiable in scenarios without real Twitter.

4. **Video URL sharing (from Python) — preserve or design fresh?**
   Python's URL-sharing across events (multiple events sharing a video_asset via video_shares) worked for the trivial case. My earlier proposal was a 3-layer dedup (URL → content hash → perceptual hash + LSH). That belongs in O4/O5, but the SCHEMA (video_assets.content_hash UNIQUE, perceptual_hash_prefix indexed) already exists in schema.sql. My lean: **keep the schema, defer design conversation to O4**.

Sign off on the 4 questions above and O3/a starts.
