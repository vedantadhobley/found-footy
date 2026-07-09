# Architectural Decisions

Append-only. Newest at top. Dates in `YYYY-MM-DD`. When migrating an old
decision because the rationale changed, leave the old entry in place and
add a new one above it pointing at the change.

---

## 2026-07-09 — apifootball adapter: bugfixes + chunk-parallel refactor

Two-part change on top of the earlier same-day docs-seeding entry.

**Part 1: bugfixes from the doc-seeding follow-ups.**

Both flagged in the earlier entry, both verified live against the
real API after fix:

- Auth header: `x-rapidapi-key` → `x-apisports-key`. Regressed
  somewhere during Phase S7.1; Python's `api_client.py:api_key`
  had always used the doc-correct name. The API tolerated the
  RapidAPI header (why Ingest worked despite the bug), but doc-
  correct + Python-matching is `x-apisports-key`.
- Rate-limit header parsing wired backwards:
  - `x-ratelimit-requests-remaining` (name has "requests") is the
    DAILY quota per doc → wired to `dailyQuotaRemain` gauge.
  - `X-RateLimit-Remaining` (no "requests") is the PER-MINUTE burst
    per doc → wired to `rateLimitRemain` gauge.
  - Old code read a phantom `x-rapidapi-requests-remaining` header
    that api-sports.io never sends.

**Part 2: ListFixturesByIDs refactor — chunk-parallel + partial return.**

Old shape: client hard-rejected >20 IDs. Workflows had their own
`chunkIDs` helper (`internal/workflow/monitor.go`) + fanned out one
Activity per chunk via `workflow.ExecuteActivity`. IngestWorkflow
comment claimed "does NOT chunk" — latent bug: a manual-override
call with >20 IDs would fail hard.

New shape: `Client.ListFixturesByIDs(ids)` accepts any size, splits
internally at `IDsBatchLimit=20` (now exported), fires per-chunk
HTTP calls in parallel via `errgroup`, returns
`(fixtures, failedIDs, err)`. Partial failure is expressed via
non-empty `failedIDs` with `err=nil`; catastrophic failure (all
chunks failed / ctx cancelled) surfaces as an actual error.

**Why partial-return over Temporal-native fan-out (workflow-side
Activity-per-chunk):** discussed at length with user before code
changes. The retry-granularity story that "Activity per chunk gives
per-chunk retry" turned out to matter less than expected for the
Monitor workload — Monitor polls every 30s, so the poll IS the
retry, and per-chunk Temporal fan-out costs 3× Temporal history for
recovery that's already ~30s. Client-side parallel goroutines +
FailedIDs return value + per-workflow retry policy gives the same
parallelism at 1/3 the Temporal overhead.

Per-workflow retry policy:

- **MonitorWorkflow**: no explicit retry. Just logs `MissedIDs`
  count. Next 30s cycle re-requests missed IDs naturally via the
  full active-fixtures pull. Cheaper than Temporal-native retry
  and equivalent in outcome.
- **IngestWorkflow (manual-ID path)**: explicit workflow-level
  loop, 3 attempts, 5s×attempt backoff, TARGETED at only the
  FailedIDs from the previous attempt. Ingest is daily — recovery
  in-cycle beats waiting 24h. Also fixes the latent bug where
  >20 manual IDs would fail hard.

**Live verification** — `scripts/verify_apifootball` hit real
`v3.football.api-sports.io` and confirmed end-to-end:

- Auth via `x-apisports-key`: OK (plan=Pro, 7500/day quota).
- 15-ID single-chunk: 15/15 back, 0 failed, one HTTP call.
- **50-ID multi-chunk: 50/50 back, 0 failed, 0.12s wall-clock** —
  three chunks (20+20+10) fired concurrently. Sequential would have
  been ~600-1500ms; parallel achieved ~5-10× speedup.
- Both rate-limit gauges populated correctly from doc-correct
  headers: `ratelimit_remaining 296` (per-minute), `daily_quota_remaining 7453`.

**Follow-up items (deferred, not blocking):**

- `FixtureListParams` missing a `Live` field — can't currently
  hit `/fixtures?live=all` through the adapter. Not needed by any
  workflow today; add when a caller wants it.
- `ListFixtures` (window/date variant) doesn't yet support the
  same failedIDs semantics; not applicable since it doesn't chunk
  (no `ids=` param).
- Verify script currently uses raw `fmt.Println` for output. If
  we start capturing verify runs into CI or Loki, promote to the
  standard log emitter.

---

## 2026-07-09 — API-Football docs archived + frozen reference seeded

Vendor docs at <https://www.api-football.com/documentation-v3> are
behind a Cloudflare bot challenge — no agent-side WebFetch or curl
UA-spoof gets through. Rediscovering the API's shape from Python +
observation had become expensive (two nights this week alone —
"Red Card" vs "Red card" casing was one of them). Fixed the whole
class of problem in one pass.

**What shipped:**
- PDF export of the docs site (v3.9.3, 131 pages) archived at
  `docs/api-football/vendor/api-football-v3.9.3.pdf` (`git mv` from
  repo root, where it was accidentally committed the night before).
- Full browser-save HTML mirror at
  `docs/api-football/vendor/api-football-v3.9.3.html` — grepable /
  searchable by agents. Asset dir gitignored (5 MB of screenshots).
- Four frozen reference docs under `docs/api-football/` seeded from
  the archive: events-shape.md, fixtures-endpoint.md, status-codes.md,
  rate-limits.md. Each cites the specific PDF page it was seeded
  from.
- `.gitignore` line 109 `vendor/` → `/vendor/` (Go convention is
  root-only; the un-anchored form would also catch the vendor-doc
  archive).

**Key findings that resolve open questions:**

1. **PST → NS on reschedule (RESOLVED — was open in 2026-07-07 entry).**
   Doc explicitly states: *"Postponed to another day, once the new
   date and time is known the status will change to Not Started."*
   Fixture IDs are immutable across this transition. Our current
   worker doesn't watch for date-field changes on active fixtures
   — a deferred behavior noted in the 2026-07-07 "fixture activation
   triggers" entry. Now that we know the transition is a real
   thing, the deferred handler is worth implementing when a real
   PST-reschedule surfaces in prod.

2. **ABD may or may not reschedule.** Doc says *"Can be rescheduled
   or not, it depends on the competition."* We treat ABD as Terminal
   (drops out of Monitor). Reschedules would only be caught by
   daily re-seeding, not in-cycle recovery. Follow-up: should ABD
   get PST-like Live treatment? Not urgent; leave until we see one
   in prod.

3. **429 is NOT documented in the /fixtures response set.** Only
   200 / 204 / 499 / 500 are documented. The doc's Rate Limiting
   Policy says *"Excess traffic may be temporarily or permanently
   blocked without notice"* — vendor-choice error, no promise of
   429. Our adapter's 429-specific handling is defensive against
   observed prod behavior, not doc-specified — kept, but the
   frozen doc now reflects reality.

4. **Rate-limit header names — two axes, easy to confuse.**
   `x-ratelimit-requests-limit` / `-remaining` are **daily**.
   `X-RateLimit-Limit` / `-Remaining` (no "requests" segment,
   mixed case) are **per-minute**. Our older `rate-limits.md`
   stub speculated `x-rapidapi-*` was daily — wrong; that's a
   different vendor's convention.

**Follow-up items surfaced (not blocking):**

- `internal/infra/apifootball/client.go:137` sets
  `x-rapidapi-key` for auth. Direct API-Sports endpoint (which
  we're on) uses `x-apisports-key` per doc page 1. The API may
  tolerate both, but the doc-correct header is the API-Sports
  one. Verify + fix.
- `internal/infra/apifootball/client.go:209` reads
  `x-rapidapi-requests-remaining` — a phantom header not in the
  API-Sports docs. Should read `X-RateLimit-Remaining` for the
  per-minute value.
- Both are silent-degrade paths (missing auth would 401; missing
  header just doesn't observe the metric) — not blocking, but
  worth a cleanup pass.

**Human update flow** for the frozen docs: when the vendor updates
the site, re-save PDF + HTML, drop into `vendor/`, sed the HTML's
asset paths, update the version in filenames, and reconcile the
seeded markdown files. Precedence for source-of-truth remains:
vendor archive > seeded markdown > Python config > adapter
observation.

---

## 2026-07-08 — Test corpus harness Phase 1a shipped + activity clock injection pattern

Ships the minimum viable scenario harness designed in
[`proposals/test-corpus.md`](./rebuild/proposals/test-corpus.md). The
"catch prod-class bugs before shipping" testing gap Python has had
forever, and that we've been building against without.

**What shipped in Phase 1a:**
- `test/harness/` — scenario loader (scenario.go), testcontainer pg
  helper (pg.go), httptest mock apifootball (mock_api.go), Tier 1
  assertion engine (assertions.go), scenario runner (runner.go)
- `test/scenarios/basic/ingest_happy_path.yaml` — first scenario
- `test/scenarios_test.go` — the test binary; iterates all YAML under
  `test/scenarios/`, dispatches each to `harness.RunScenario`
- `Makefile` — new `test-corpus` target

**First scenario passing end-to-end.** Total runtime: ~2-3s (mostly
testcontainer pg boot). Actual scenario execution: ~40ms. Full
corpus of 50 scenarios projected <90s per the proposal target.

**Real bug caught during first-run** — the harness immediately
surfaced a determinism issue: `ingest.Activities.now()` reads
wall-clock time, so a scenario with kickoffs relative to
2026-07-07 would non-deterministically activate fixtures whose
kickoff is "in the past" relative to today's real clock.

**Fix pattern established: activity clock injection from scenario.**
The runner sets `acts.Now = func() time.Time { return
scenario.ManualDate.UTC() }` when a scenario declares a `manual_date`.
Activities that need "now" go through this injected clock; scenarios
control what time the activity sees. Every new activity (monitor,
discovery, etc.) MUST expose an injectable clock — same discipline
as `ingest.Activities.Now`. Non-injectable clocks are a determinism
bug caught immediately by the harness on first scenario.

**Why this matters strategically:**
The user's currently-live prod pain ("goal stuck in debounce, video
not found for 5 minutes") is exactly the class of bug the corpus can
catch — interaction of API flakiness with debounce state can be
reproduced in a scenario and asserted against forever. Once we have
Loki logs of a prod bug, the FIRST step becomes "write a regression
scenario that reproduces it" — then we can iterate on the fix with
tight feedback.

**Design decisions logged during implementation:**
- **Truncate between scenarios** (not scenario-prefixed IDs). Chose
  simplicity + guaranteed clean slate.
- **Scenarios at `test/scenarios/`** (repo root). Not `internal/` —
  integration-test-scoped.
- **Every scenario runs on every push via `make test`.** Corpus is
  fast enough not to gate behind nightly.
- **Suites are subdirectories** under `test/scenarios/`. Test name
  includes the suite path so filtering works:
  `go test -run TestScenarios/basic ./test`.
- **Scenario file specifies `workflow: <Name>`** — runner dispatches
  to per-workflow handlers. Currently only "IngestWorkflow"
  supported; adding MonitorWorkflow just extends the switch.

**Deferred to future phases:**
- Tier 2 assertions (workflow spawns, video shares, semantic events)
  — build when scenarios need them
- Tier 3 (log lines, metrics, timing bounds) — same
- Per-cycle API responses (currently one blob per scenario) — extend
  when Monitor scenarios arrive
- Real Temporal server (currently in-memory testsuite) — probably
  never; live smoke via `scripts/trigger_ingest` covers that gap

Next scenarios to write (before Monitor code lands, so the code
comes to fit these):
- `debounce/var_overturn.yaml` — count 1→2→3→trigger→2→1→0→soft-delete
- `debounce/flicker_no_reset.yaml` — the symmetric counter's
  differentiator vs Python
- `basic/ingest_manual_ids.yaml` — manual re-ingest path
- More as needed.

---

## 2026-07-07 — Symmetric-counter debounce (Go rebuild's improvement over Python)

Designed during O2 planning, implemented in fix 3b. Replaces Python's
asymmetric two-array model (`_monitor_workflows` monotonic + reset-on-
presence `_drop_workflows`) with a single counter that oscillates
0..3, incrementing on presence votes and decrementing on absence
votes.

**Model (single counter per event):**
- `debounce_count` INT in `events` table, CHECK `BETWEEN 0 AND 3`
- Seeded at 1 by `Insert(event, workflowID)` — the first-see IS the
  first vote
- Presence vote (from a new workflow_id) increments (LEAST +1, cap 3)
- Absence vote (from a new workflow_id) decrements (GREATEST -1,
  floor 0)
- Vote idempotency enforced by PRIMARY KEY on
  (event_id, workflow_id) in `event_monitor_workflows` and
  `event_drop_workflows` — retrying activities can't double-count
- On first crossing to 3: `downstream_triggered` flag flips
  FALSE→TRUE atomically (one-way). Caller spawns downstream
  workflows exactly once.
- On hitting 0: same transaction atomically soft-deletes with
  `removed=TRUE, removed_reason='var', removed_at=NOW()`. Caller
  runs the destroy pipeline (Temporal cancel + video_shares
  soft-delete) — that's a separate activity, not a repo method.

**Differences from Python** (see also `archive/src/data/events.py`
`add_monitor_workflow`, `clear_drop_workflows`,
`add_drop_workflow_and_check`):

1. **Symmetric vs asymmetric.** Python: monitor votes monotonic,
   drop votes fully reset on any presence. Ours: single counter
   steps up on presence, down on absence, cap and floor both
   enforced.
2. **Flicker penalty.** Python: 3 CONSECUTIVE absences required
   for delete (any presence resets the drop tally). Ours: 3 NET
   absences from wherever counter sits. Flicker accumulates.
3. **Hard vs soft delete.** Python deletes the event row. Ours
   sets `removed=TRUE` — preserves audit trail + prevents
   natural_key collision on reappearance (soft-removed row still
   holds the natural_key; monitor's collision handler recognizes
   the removed state and skips re-voting).
4. **`downstream_triggered` semantics.** Python's `_monitor_complete`
   flag stays TRUE forever. Ours: same one-way flip semantic. No
   change here — this part matches.

**Trade-offs of ours vs Python's:**
- Ours penalizes flicker. Two absences with a presence in between
  chips away at durability. Python: the presence would erase both.
  User's call: they prefer explicit accounting over the reset.
- Ours easier to reason about — one counter, symmetric transitions.
- Ours worse under sustained API flakiness (2 absences early cost
  budget that persists). Fine if API is generally reliable.

**Post-removal behavior — event never returns.** Terminal state.
Even if the API brings the same natural_key back, the collision
handler (to be written in monitor activity code) recognizes the
removed row and skips. If a DIFFERENT event with a similar
signature (same player scoring again, say) happens later, it gets
a new seq via natural_key sequencing — no collision.

**Schema additions to `internal/infra/pg/schema.sql`:**
```sql
events.debounce_count INT NOT NULL DEFAULT 1
  CHECK (debounce_count BETWEEN 0 AND 3)
events.downstream_triggered BOOLEAN NOT NULL DEFAULT FALSE
```

**Interface changes** (`internal/domain/event/repo.go`):
- `Insert(ctx, e)` → `Insert(ctx, e, workflowID)` — atomic
  event+vote seed
- ADDED `RegisterEventPresence(ctx, eventID, workflowID) →
  (newCount, justTriggeredDownstream, err)`
- ADDED `RegisterEventAbsence(ctx, eventID, workflowID) →
  (newCount, hitZero, err)` — soft-deletes atomically at zero
- REMOVED (were unimplemented): `RegisterMonitorWorkflow`,
  `RegisterDropWorkflow` — subsumed by presence/absence
- KEPT UNCHANGED: `RegisterVideoValidationWorkflow` — separate
  concern (tracks download attempts, not stability)

**Test coverage** (all pass against testcontainer Postgres):
- Presence climb 1→2→3, only third call justTriggered
- Presence idempotency (same workflow_id retries)
- Presence no-retrigger (cap at 3 post-flip)
- Absence hits zero → soft-delete atomic
- Absence 3→2→1→0 sequence, only last hitZero
- Flicker (present-absent-present) does not hard-reset
- Absence idempotency
- Post-removal absence is no-op (no double soft-delete)

Total: 8 new debounce tests + updated 6 existing CRUD tests for the
Insert signature change.

Devs need to wipe pg volume for schema change:
`docker volume rm found-footy-dev_postgres-data && docker compose
-f docker-compose.dev.yml up -d postgres`. Done in dev; prod
unaffected (still runs Python).

---

## 2026-07-07 — APIStatus bucketing preserves Python's SUSP/INT/PST=active

Preserves Python's status classification (`archive/src/utils/fixture_status.py`)
for the Go rebuild. Load-bearing detail I initially got wrong:

**Python's active bucket**: `1H, HT, 2H, ET, BT, P, LIVE, SUSP, INT,
PST` — the obvious playing codes PLUS three "not-currently-playing
but might resume any minute" codes.

**My initial Go implementation (`internal/domain/fixture/fixture.go`
`APIStatus.Live()`)** was missing `SUSP`, `INT`, `PST`. That would
have caused MonitorWorkflow to skip these fixtures when they should
be polled — real bug, caught during O2 planning.

**Fix**: added `SUSP, INT, PST` to `APIStatus.Live()`. Matches Python
exactly. Test updated to cover the new codes explicitly.

**Why preserve Python's design** (not overhaul):
- Cost isn't wasteful: our API calls are BATCHED via `?ids=...`.
  Adding a PST fixture to the batch costs 0 additional API calls.
  My earlier "PST fixtures burn API budget" concern was wrong; the
  batching model already makes this free.
- Short delays (15-30 min) are common in real matches. Polling PST
  fixtures every 30s means we catch the resume within one cycle.
- Only truly-lost postponed fixtures (never resume) cost anything,
  and those get handled by daily ingest / a future cleanup job.

**Consequences downstream**:
- MonitorWorkflow's active-fixture-list query stays `state = active`
  regardless of underlying api_status. No distinct `postponed` state
  added.
- A fresh fixture that ingest sees with `status=PST` gets emergency-
  activated (same as if `status=1H`). Same-day resume within 30 min
  of pre-activation window fires normally.
- The "stuck PST forever" edge case is a real but small residue —
  handled separately by a future cleanup (Python does this via a
  "next day cleanup" I haven't yet grepped).

**Doc updates in same commit** (per working rule):
- `internal/domain/fixture/fixture.go` — `APIStatus.Live()` +
  docstring explaining the classification + Python source cite
- `internal/domain/fixture/fixture_test.go` — test now covers all
  10 Live codes explicitly
- `docs/rebuild/orchestration.md` — IngestWorkflow initial-state
  paragraph updated to reflect the full Live() code set

**Not decided in this entry** (queued for later O2 questions):
- Whether to add adaptive polling frequency at all
- The "next day cleanup" for stuck PST — need to read Python's impl
  first

---

## 2026-07-07 — O1e complete — schedule registered + all §5 W1 divergences realigned

Closes the O1e sequence started after the retro. All six IngestWorkflow
divergences from plan §5 W1 (see
[original entry](#2026-07-07--ingestworkflow-divergences-from-plan-5-w1))
are now either fixed or explicitly kept-with-rationale.

**O1e/b — daily Temporal Schedule wired.** `cmd/worker/main.go`
`ensureIngestSchedule` runs on worker startup and registers the
schedule if it doesn't exist. Schedule ID
`ingest-scheduled-daily`; cron `5 0 * * *` (00:05 UTC); overlap
policy SKIP; args `IngestWorkflowInput{RetentionDays: 14}` (plan
§5 W1 default).

Idempotent: `ErrScheduleAlreadyRunning` on subsequent restarts is
caught + logged as an "already exists" outcome (new vocabulary
action `temporal_schedule_already_exists`) rather than an error.
Manual updates via `temporal schedule update` are safe.

Verified live in dev:
  ✓ Schedule created on first startup after deletion
    (log: temporal_schedule_created)
  ✓ Schedule "already registered" on restart
    (log: temporal_schedule_already_exists)
  ✓ `temporal schedule list` shows next run 8h from now with the
    expected cron

**Adapter changes:**
- `internal/infra/temporal/client.go`:
  `Client.ScheduleClient() client.ScheduleClient` — passthrough
  accessor to the SDK's ScheduleClient. Not per-op instrumented
  (schedule ops are rare).
- `internal/observability/vocabulary/actions_infra_temporal.go`:
  three new actions — `temporal_schedule_created`,
  `temporal_schedule_already_exists`, `temporal_schedule_failed`.

**Final status of the original 6 divergences from
[2026-07-07 IngestWorkflow entry](#2026-07-07--ingestworkflow-divergences-from-plan-5-w1):**

| # | Divergence | Resolution |
|---|---|---|
| 1 | Input shape | Realigned in O1e/a (fixed) |
| 2 | Output shape (Errors []string) | Realigned in pre-O1e cleanup batch (fixed) |
| 3 | ListUniqueTeamsFromFixtures inlined | Kept — defensible improvement |
| 4 | RAG deferral (PreCacheAliasesBatch → placeholder) | Kept — user-approved deferral |
| 5 | ManualFixtureIDs missing | Fixed as part of O1e/a |
| 6 | ActivationWindow added | Kept — user-approved addition |

**Doc updates in same commit** per working rule:
- `docs/rebuild/deployment.md` — replaces "Workflow scheduling — NOT
  WIRED" section with the wired-and-verified pattern
- `docs/rebuild/temporal.md` — replaces "Schedule registration —
  NOT YET WIRED" with the actual pattern + load-bearing invariants
  (idempotency, no-overwrite, overlap SKIP)

**Files:**
- `cmd/worker/main.go` — `ensureIngestSchedule` + imports
- `internal/infra/temporal/client.go` — ScheduleClient accessor
- `internal/observability/vocabulary/actions_infra_temporal.go` — 3
  new actions
- `docs/rebuild/deployment.md` — schedule section
- `docs/rebuild/temporal.md` — schedule section

Phase O1 is now genuinely complete — including doc discipline,
input/output realignment, and schedule wiring. Ready for O2
(MonitorWorkflow) after any planning discussion.

---

## 2026-07-07 — O1e/a — IngestWorkflow input reshape complete

Realigns three of the six IngestWorkflow divergences from plan §5 W1
that the retro logged (see [earlier entry](#2026-07-07--ingestworkflow-divergences-from-plan-5-w1)).

**Changes:**

**1. Input shape → plan §5 W1 + ActivationWindow.**
```go
type IngestWorkflowInput struct {
    ManualDate       *time.Time
    ManualFixtureIDs []int64
    ActivationWindow time.Duration
    RetentionDays    int
}
```
Was:
```go
type IngestWorkflowInput struct {
    FetchWindowFrom    time.Time
    FetchWindowTo      time.Time
    ActivationWindow   time.Duration
    RetentionThreshold time.Time
}
```
Behavior: workflow computes fetch window from anchor (ManualDate or
workflow.Now) using plan's `[anchor-1d, anchor+3d]` bracket. Retention
cutoff is `anchor - RetentionDays*24h`. Everything derives from the
anchor so manual-date re-ingest is consistent.

**2. `ManualFixtureIDs` path added.** When populated, workflow
dispatches to a new `FetchFixturesByIDs` activity (thin wrapper over
`apifootball.ListFixturesByIDs`, existing adapter method from O1a).
`FetchFixturesForWindow` is bypassed entirely. Cap: 20 IDs per call
(api-sports.io limit; workflow does NOT chunk — callers must batch).

**3. `RetentionDays int` replaces `RetentionThreshold time.Time`.**
Zero still means "skip prune." The schedule spec sends 14 explicitly
(not injected as default at workflow level — callers own the value).

**Test updates:**
- Two new activity tests: `TestFetchFixturesByIDs_HappyPath`,
  `TestFetchFixturesByIDs_PropagatesError`
- Two new workflow tests: `TestIngestWorkflow_ManualFixtureIDs_UsesByIDsPath`,
  `TestIngestWorkflow_EmptyInput_UsesDefaults`
- Updated `stdInput` helper + `TestIngestWorkflow_ZeroRetention_SkipsPrune`
  for the new shape
- Total ingest+workflow tests: 23 (was 19), all passing

**`scripts/trigger_ingest/main.go` simplified:** now passes empty
input; workflow self-configures with `workflow.Now` as anchor + skip
prune (dev safety default).

**Doc updates in same commit** per working rule:
- `docs/rebuild/orchestration.md` — new input shape + branching
  activity sequence + anchor propagation notes.

**Two divergences from the original 6 still open:** input reshape (this
entry — DONE) and output reshape / Errors []string (done in the
[2026-07-07 pre-O1e cleanup batch](#2026-07-07--pre-o1e-cleanup--lastpolledat-fix--errors-string)).
Remaining unfinished O1e item: schedule registration (O1e/b, queued).

---

## 2026-07-07 — Pre-O1e cleanup — LastPolledAt fix + Errors []string

Small pre-O1e batch, three changes to `internal/activity/ingest/`:

**1. `LastPolledAt` now set on all ingest paths.** Was a real bug:
ingest hit api-sports.io (that IS a poll) but the fixture's
`LastPolledAt` field stayed nil on fresh rows and stale on existing
rows. Consequences would have been: MonitorWorkflow's future poll-
bucket logic re-polls every fixture on its first cycle after ingest,
wasting the amortization the whole scheme exists to provide. Fixed
on both branches of `reconcileFixture` (existing: `existing.LastPolledAt
= &now`; fresh: `f.LastPolledAt = &now` before state transitions,
which don't touch that field). Two new regression tests
(`TestCategorize_SetsLastPolledAt_OnFresh`,
`TestCategorize_UpdatesLastPolledAt_OnExisting`).

**2. Removed redundant `f.CreatedAt = now; f.UpdatedAt = now`** from
`reconcileFixture` fresh branch. `fixture.New` sets both internally
via `time.Now().UTC()`; state transitions (Activate/Complete)
overwrite UpdatedAt anyway. Manual re-sets were dead code. Off-by-ns
CreatedAt drift vs the injected test `now` is harmless (no test
asserts on it — the field is a "when was this row born" audit
signal).

**3. `Errors int` → `Errors []string` with context.** Was one of
the 6 IngestWorkflow divergences from plan §5 W1 logged in the retro
as "silent, realign in O1e." Doing it now as part of the small
batch. Per-fixture / per-team failures inside activity loops now
land as strings like `"reconcile fixture=1515514: pool exhausted"`,
aggregated at the workflow level into `IngestWorkflowOutput.Errors
[]string`. Operators see WHAT failed and WHY in Temporal UI without
joining logs. Deleted the workflow's `CategorizeErrors int` and
`AliasErrors int` fields (subsumed by the aggregated Errors slice).
One new test (`TestCategorize_ErrorsCarryFixtureContext`).

**Doesn't touch input shape.** Full O1e (input reshape to
`ManualDate + ManualFixtureIDs + RetentionDays`, schedule
registration) is still queued. This batch fixes the specific gaps
the user surfaced without expanding scope.

**Doc updates** (per the same-commit rule):
- `docs/rebuild/orchestration.md` — new I/O shape + LastPolledAt
  notes in the reconcile-logic section.

**Files:**
- `internal/activity/ingest/activities.go` — the three changes
- `internal/activity/ingest/activities_test.go` — 3 new tests
- `internal/workflow/ingest.go` — `IngestWorkflowOutput.Errors`
  aggregation
- `internal/workflow/ingest_test.go` — no changes needed (mocks
  don't reference removed fields)

Test count: 19 in ingest + workflow (was 16), all passing.

---

## 2026-07-07 — Ripped `internal/errors/` stub

Plan §2 tree listed `internal/errors/ # typed error taxonomy`. Shipped
state until today: a 5-line `doc.go` with no types, no functions, no
imports from anywhere in the codebase.

**Decision:** delete the file + directory. Reconsidered under the
`docs/rebuild-plan.md` = intent / `docs/rebuild/*.md` = ledger
framing: an empty stub sends a **false signal** that "typed error
taxonomy lives here" when it doesn't. Every adapter defines its own
error types locally (LLM has typed errors, apifootball has HTTP
status classification, etc.). Cross-cutting sentinels haven't been
needed yet because no workflow branches on error class — IngestWorkflow
uses uniform exponential backoff regardless of failure mode.

**When to rebuild:** when the first workflow (probably MonitorWorkflow
in O2, more likely DiscoveryWorkflow in O3) needs `errors.Is(err,
ffserrors.ErrRateLimited)` to distinguish "back off harder" from
"give up." At that point create `internal/errors/errors.go` fresh
with only the sentinels we actually need. Each adapter's own errors.go
wraps with `fmt.Errorf("apifootball: quota exhausted: %w",
ffserrors.ErrRateLimited)`.

**Trigger criteria:** a workflow's retry policy needs to branch on
error class, OR two different adapters emit conceptually-identical
errors (e.g. both LLM and apifootball emit rate-limit) that a
workflow needs to treat uniformly.

**Not building this now on the "plan said so" reason.** Follows
the CLAUDE.md rule against half-finished implementations. Follows the
"don't add abstractions beyond what the task requires" rule.

Ripped in the same commit as this entry. Two-second `git rm`; two-second
recreation later when actually needed.

---

## 2026-07-07 — Working rule: living docs update in the same commit as code

Retrospective response to a stretch of ~15 commits (S1–O1d) where code
shipped without corresponding updates to `docs/rebuild/*.md`.

**Rule.** Every implementation commit that adds/changes a package,
adapter shape, workflow, or activity MUST update the relevant living
doc (usually `docs/rebuild/architecture.md` or
`docs/rebuild/orchestration.md`) in the SAME commit. If it diverges
from `docs/rebuild-plan.md`, `docs/decisions.md` gets an entry.

**Why.** The plan is intent — 12k lines, written before implementation,
not per-commit. The per-topic docs in `docs/rebuild/` were meant to be
the ledger of what actually shipped and are currently empty stubs.
Silent code-only commits meant the plan drifted from reality without
either doc surfacing that fact. This retro compensates. Going forward,
the rule prevents recurrence.

**Enforcement.** An implementation commit without a corresponding doc
update is treated as incomplete — same status as one missing tests.

---

## 2026-07-07 — Doc retro closure

Sixth and final commit in the retrospective doc-fill sweep that began
after realizing S1–O1d shipped without living-doc updates.

**What got done (6 commits over ~2 hours):**

1. `docs/rebuild/architecture.md` — filled with as-shipped tree,
   per-package status, adapter template.
2. `docs/rebuild/orchestration.md` — filled with workflow inventory
   + IngestWorkflow ledger.
3. `docs/rebuild/observability.md` + `logging.md` — filled with
   four-pillars status + vocabulary + Emit reference. (Note: first
   attempt at this had a broken commit — Write failed silently,
   commit message lied; caught + fixed in follow-up.)
4. `docs/rebuild/temporal.md` — filled with Client/Worker shape +
   registration flow.
5. `docs/rebuild/testing.md` + touched-up `deployment.md` — filled
   test tier ledger.
6. `CLAUDE.md` phase table + `docs/rebuild/README.md` status +
   this closure entry.

**Divergences logged in this sweep (5 groups):**

- Working rule (this rule)
- Architecture divergences from plan §2 (7 items: migrations empty,
  bootstrap added, 4 internal/ packages stubbed, 4 domain packages
  stubbed)
- IngestWorkflow divergences from plan §5 W1 (6 items — 3 sanctioned,
  3 silent, 3 slated for realignment in O1e)
- Log-catalog generator §11.3 deferred (rationale: grep is still
  ergonomic at ~50 actions)
- Temporal adapter divergences from plan §9 (5 items — 4 kept as
  improvements, 1 sensible addition)

**Stubs NOT filled** (deferred with rationale, not omissions):
- `docs/rebuild/api-contract.md` — Phase A hasn't shipped; nothing
  to ledger yet.
- `docs/rebuild/operations.md` — Phase M/C bring-up procedures
  aren't real yet.

**What resumes:** Phase O2 planning (MonitorWorkflow) — but only
after O1e realignment commits fix the input/output-shape divergences
from plan §5 W1 that this retro surfaced. Order:

1. **O1e/a**: Realign IngestWorkflow input to plan shape
   (ManualDate + ManualFixtureIDs + RetentionDays) — with tests +
   scripts/trigger_ingest update + doc update.
2. **O1e/b**: Realign IngestWorkflow output to include Errors []string.
3. **O1e/c**: Register daily Temporal Schedule for IngestWorkflow.
4. **Only then**: O2 begins.

Retro complete. Living-doc-with-code discipline stands as the working
rule going forward.

---

## 2026-07-07 — Temporal adapter divergences from plan §9

Three divergences between `internal/infra/temporal/` (shipped in S5)
and plan §9's temporal spec. Retrospectively logged as part of the
doc retro.

**1. `NewClient` takes `*Instruments`, not `*slog.Logger`.** Plan §9
had `func NewClient(ctx, cfg, logger *slog.Logger) (client.Client, error)`.
Shipped: `func NewClient(ctx, cfg, ins *Instruments) (*Client, error)`.
**Silent.** Instruments carry logger + metrics + (eventually) tracing
together; the plan's logger-only param would be an outlier vs every
other adapter (S2+). **Keep — consistent with adapter template.**

**2. `Client` wraps SDK type; doesn't return raw `client.Client`.**
Plan §9 wanted `(client.Client, error)` return. Shipped: `(*Client,
error)` with our type. **Silent.** Rationale: (a) `WorkerShutdownTimeout()`
accessor for graceful shutdown ordering, (b) our `Close()` hook can
emit metrics, (c) tracing hookup later without changing callers.
**Keep.**

**3. `worker.Options` param on `NewWorker`; no hardcoded defaults.**
Plan §9 wanted `NewWorker(c, cfg, logger)` returning a worker with
"sensible defaults" (MaxConcurrentActivityExecutions:30,
MaxConcurrentWorkflowTasks:10) baked in. Shipped:
`NewWorker(c, ins, options worker.Options)` with caller-supplied
options. **Silent.** Rationale: cmd binaries know what they're
running; adapter shouldn't decide concurrency. **Keep.**

**4. `DefaultRetryPolicy()` helper not shipped.** Plan §9 called for
`func DefaultRetryPolicy() *temporal.RetryPolicy`. Not implemented.
Each workflow defines its own `workflow.ActivityOptions` inline
(e.g. `internal/workflow/ingest.go`). Rationale: retry policy visible
at call site is easier to audit than a shared default that hides its
config. **Keep the divergence; delete the plan's helper on next plan
revision.**

**5. `SignalWorkflow` method added on Client.** Not in plan §9.
Sensible addition; kept for future AssetPersistenceWorkflow signal
paths.

---

## 2026-07-07 — Log-catalog generator (§11.3) not shipped

Plan §11.3 specifies a generated `docs/generated/log-catalog.md`
regenerated on every build via `go generate`, listing every
(vocabulary.Module, vocabulary.Action) pair with expected field set
and log-level guidance.

**Status:** not implemented in S1 or since.

**Rationale for the deferral:** with ~50 declared actions across
11 adapter modules + 5 workflow modules today, `grep -r "vocabulary\.Action"`
is still ergonomic. Generator adds a build-time dependency (needs a
tool + a generate marker + a CI step to keep the checked-in catalog
current) that's cheap to build later when the surface grows and hard
to justify now.

**Trigger to build:** when the vocabulary passes ~200 declared
actions OR when someone asks "what actions does X module emit?" and
grep isn't fast enough OR when we introduce webhook consumers that
need a discoverable catalog to subscribe to.

**Not a design change.** The plan's shape (per-(module, action)
markdown table) still stands; only the timing shifts.

---

## 2026-07-07 — IngestWorkflow divergences from plan §5 W1

Six divergences between the shipped IngestWorkflow (O1a-O1d) and the
plan's §5 W1 spec. Retrospectively logged as part of the
2026-07-07 doc retro. Three are user-approved conversationally,
three were silent implementation choices — decisions on whether to
keep or realign made below.

**1. Input shape.** Plan wanted:
```go
type IngestWorkflowInput struct {
    ManualDate       *time.Time   // nil for scheduled; set for manual re-run
    ManualFixtureIDs []int64      // nil for full-window; set for ad-hoc re-ingest
    RetentionDays    int          // default 14
}
```
Shipped:
```go
type IngestWorkflowInput struct {
    FetchWindowFrom    time.Time
    FetchWindowTo      time.Time
    ActivationWindow   time.Duration
    RetentionThreshold time.Time
}
```
**Silent.** My choice, not discussed. Consequences: no ad-hoc
per-ID re-ingest (already bit me — `scripts/trigger_ingest`
hardcodes a window because `ManualFixtureIDs` doesn't exist).
Explicit window params are more general but every caller (schedule +
manual trigger) has to compute them; plan's `ManualDate` +
schedule-default was simpler for the common case.
**Decision:** realign to plan shape in **O1e follow-up commit**.
Add back `ManualFixtureIDs` for ad-hoc re-ingest. Keep
`ActivationWindow` as an addition (see #6). Convert
`RetentionThreshold time.Time` back to `RetentionDays int`
(schedule computes cutoff from days).

**2. Output shape.** Plan wanted:
```go
type IngestWorkflowOutput struct {
    FixturesUpserted int
    AliasesCached    int
    FixturesPruned   int
    Errors           []string  // non-fatal errors surfaced for observability
}
```
Shipped: 9 int counters (staging, active, completed, existing,
inserted, aliasErrors, categorizeErrors, prunedFixtures, fetched).
No `Errors []string`.
**Silent.** More granular but loses the observability signal for
non-fatal per-fixture errors that don't fail the workflow.
**Decision:** realign in O1e. Keep the granular counts as fields
alongside `Errors []string`. Both are useful.

**3. `ListUniqueTeamsFromFixtures` inlined into
`CategorizeAndUpsertFixtures`.** Plan had it as a separate activity
step. Shipped: collected via a `map[int]TeamRef` inside the
categorize loop, returned as part of `CategorizeOutput.TeamRefs`.
**Silent.** Rationale for the choice: dedup-by-team-id happens in
memory; extracting it to a separate activity means re-marshaling
the full `[]APIFixture` across the Temporal activity boundary for
what's effectively a pure map-reduce.
**Decision:** keep. Defensible improvement over the plan.
Documented in `docs/rebuild/orchestration.md`.

**4. `PreCacheAliasesBatch` → `EnsureAliasPlaceholders`.** Plan
had `PreCacheAliasesBatch` doing full RAG resolution (Wikidata
lookup + LLM twitter-alias inference) inline during ingest.
Shipped: `EnsureAliasPlaceholders` inserts blank-resolution
placeholder rows; a separate resolution job (design TBD) fills
them later.
**User-approved conversationally.** Rationale: (a) RAG on hot
ingest couples the daily job to joi + Wikidata availability; (b)
the RAG design itself needs revisiting (team-alias + player-name
search-string building could be improved over Python's approach);
(c) user explicitly said "don't just port Python's logic here."
**Decision:** keep. Retroactively documented.

**5. No `ManualFixtureIDs` support.** Direct consequence of #1.
Plan had it as first-class input for ad-hoc re-ingest of specific
fixtures.
**Decision:** realign as part of #1 fix.

**6. `ActivationWindow` parameter added — not in plan.**
Introduced to fix the Python-era "manual ingest at 14:55 for 15:00
kickoff sits in staging until next monitor cycle" bug (see
[2026-07-07 Fixture activation triggers](#2026-07-07--fixture-activation-triggers--staging-poll-design)).
**User-approved conversationally.** Designed together during
Phase D.
**Decision:** keep. Retroactively documented.

---

## 2026-07-07 — Rebuild architecture divergences from plan §2

Documented here as part of the retrospective (see rule above). Most of
these emerged silently during S1–S7 and D and only got captured after
the fact.

**Divergences from `rebuild-plan.md` §2 tree:**

1. **`migrations/` is empty; schema lives in
   `internal/infra/pg/schema.sql`.** Plan wanted golang-migrate style
   SQL files at repo root. Instead, we ship a single `schema.sql` that
   docker-entrypoint-initdb.d mounts into dev postgres (fresh volume
   only) and testcontainers loads via `WithInitScripts`. Rationale:
   pg pool + schema landed in S2; no migrations tooling was needed
   because dev + test rebuild the DB from scratch. Prod migration
   tooling is a Phase M concern (not yet designed).
   **Decision:** keep `internal/infra/pg/schema.sql` as the
   authoritative source through Phase M. Introduce golang-migrate (or
   equivalent) alongside the Phase M cutover plan.

2. **`internal/bootstrap/` package added — not in the plan tree.**
   Contains `bootstrap.Run(...)` (shared binary startup: config load,
   signal wiring, metrics server, LIFO Closer registry) + `Deps`
   struct passed to each cmd's main. Introduced in S1 to eliminate
   ~50 lines of boilerplate per binary and centralize graceful-shutdown
   ordering. **Decision:** keep — the LIFO Closer registry pattern is
   load-bearing for temporal worker draining (worker Stop must run
   before pg pool Close).

3. **`internal/infra/event/` composer stubbed** (only `doc.go`).
   Plan wants the semantic-event dual-write composer (pg + nats) here.
   Deferred to Phase O2 when MonitorWorkflow starts emitting
   `event.detected` / `event.stable` / `event.removed`.

4. **`internal/infra/ffmpeg/` stubbed** (only `doc.go`). Deferred to
   Phase A (video pipeline).

5. **`internal/usecases/` stubbed** (only `doc.go`). Deferred to when
   the first cross-domain operation surfaces (probably VAR removal
   during O2/O3).

6. **`internal/testutil/` empty.** Build factories/fakes when second
   activity or workflow package needs sharing. Currently the ingest
   activity's fakes are inlined in its test file — cost of premature
   extraction > cost of duplication at N=1.

7. **Domain packages `discovery`, `vision`, `session`, `textanalysis`
   stubbed** (only `doc.go` each). Explicit conversation-approved
   deferral: build each when the corresponding workflow needs it.
   Rationale — building all 8 domain packages up front risks
   speculative modeling; building on demand keeps the shapes tight to
   real callers.

**No divergence** from plan §2 tree for: `cmd/`, `internal/domain/`
(the shipped 4), `internal/workflow/`, `internal/activity/`,
`internal/api/`, `internal/config/`, `internal/observability/`,
`internal/scaler/`, adapter tree (except the two composer stubs
above), `caddy/`, Dockerfiles, Makefile.

(Note: `internal/errors/` was on this list at retro time but has
since been ripped — see [2026-07-07 later entry](#2026-07-07--ripped-internalerrors-stub).)

---

## 2026-07-07 — Fixture activation triggers + staging-poll design

**Fixture activation** (staging → active) fires from three triggers,
all routed through the same `Fixture.Activate(at)` primitive:

1. **Ingest-time pre-activation.** At upsert time, if
   `f.ShouldActivateNow(now, 30min)` is true, the ingest activity
   calls Activate BEFORE the first `repo.Upsert`. The fixture never
   lands in staging in the DB. Fixes the 2026-05 Python-era bug
   where manual ingest at 14:55 for a 15:00 kickoff sat in staging
   until the next 15-min monitor cycle.
2. **Monitor pre-activation.** `MonitorWorkflow`'s
   `PreActivateUpcoming(30min)` activity scans staging fixtures every
   30 seconds and promotes any that ShouldActivateNow returns true
   for. Uses the same helper as (1) so the rule stays one place.
3. **Emergency activation.** If the monitor's API response for a
   staging fixture shows `APIStatus.Live()`, promote immediately
   (already-live match wasn't caught by pre-activation because
   ingest had the wrong kickoff or the API published a corrected
   one). Same `Activate(now)` transition.

**Staging-poll design.** The plan §5.2 as-drafted skipped the
Python-era 15-minute staging API poll, meaning postponements +
kickoff changes on staging fixtures would go undetected until the
fixture activated (which may never happen if the new kickoff is far
out). This was already noted in [`docs/todo.md`](./todo.md) as a
deferred Go-rebuild item after the 2026-07-05 Mexico vs England
postponement surfaced the gap.

Decision for Phase O's MonitorWorkflow: **mirror Python's approach.**
Every 30-second cycle:

- Always: `PreActivateUpcoming(30min)` (DB-only check, no API call)
- Always: fetch API status for active fixtures
- **On 15-min aligned boundaries** (`:00 / :15 / :30 / :45`): also
  fetch API status for staging fixtures whose `LastPolledAt` doesn't
  match the current 15-min interval. Detects postponements,
  kickoff changes, status-flipped-to-live. Per-fixture skip keeps
  the total API burn near-zero when nothing's changing.

The alignment logic is a monitor-workflow concern (schedule decision,
not domain), so it doesn't touch the fixture domain package. What
domain does provide: `ShouldActivateNow`, `APIStatus.Live()`,
`Activate`. Those three primitives compose to cover all three
triggers.

## 2026-07-07 — Workflow renames for Phase O

The Go rebuild renames three of the six Python-era Temporal workflows.
Nothing runs under the Go names yet — workflow IDs live in Temporal
history permanently, so renaming after Phase O ships would leave a
mixed vocabulary in perpetuity. Renaming now = free.

| Python name         | Go rebuild name              | Reason |
|---------------------|------------------------------|--------|
| `IngestWorkflow`    | `IngestWorkflow`             | unchanged — does exactly what it says |
| `MonitorWorkflow`   | `MonitorWorkflow`            | unchanged — poll active fixtures every 30s |
| `TwitterWorkflow`   | **`DiscoveryWorkflow`**      | "Twitter" describes the source; the workflow's job is discovering candidate videos. Rename lets a future YouTube/TikTok/Reddit source fold in without a rename cascade. |
| `DownloadWorkflow`  | **`VideoValidationWorkflow`** | downloads + AI-validates + hashes. "Download" undersells what the workflow does. |
| `UploadWorkflow`    | **`AssetPersistenceWorkflow`** | dedups first, THEN maybe uploads. The dedup is the load-bearing part; "Upload" hides it. |
| `RAGWorkflow`       | **(folded into `IngestWorkflow` as a sub-activity)** | "RAG" is implementation vocabulary. Alias resolution is one step of ingest, not a separate top-level workflow. |

Naming for Temporal task types + workflow IDs follows the new names.

## 2026-07-02 — NATS is metadata-plane only; video bytes go over HTTP

**Decision:** NATS/JetStream carries semantic events, SSE fan-out, and
webhook delivery — all small structured messages. Video bytes are NEVER
routed through NATS. Browsers fetch videos via HTTP/HTTPS directly from
Garage (S3-compatible) through the workspace Caddy, using presigned URLs
returned by the `/api/v1/videos/{share_id}` 302 redirect.

**Why NATS is wrong for video byte transport:**

- **Broker memory + storage blowup**: even at 20 MB per clip and hundreds
  of clips per match day, pushing videos through NATS turns the
  workspace-shared broker into a media store. Wrong tool for the shape.
- **No range-request support**: browsers need `Range: bytes=x-y` to seek
  within a video. HTTP/S3 does this natively; NATS doesn't.
- **Poor latency**: NATS adds a hop the CDN model avoids. Playback
  needs the shortest possible path from bytes-on-disk to browser
  decoder.
- **Wrong caching model**: Caddy + Garage can add edge caching, ETags,
  and Cache-Control; NATS as a message broker doesn't have these
  primitives.

The common misconception is that "large streaming services use Kafka
for video." They don't. Netflix / YouTube / Twitch use Kafka (or
equivalent) for the **metadata plane** — view counts, recommendation
events, ad tracking, telemetry. Bytes always go over HTTP-based
segment protocols (HLS, MPEG-DASH) backed by S3-alike storage + CDN.
Kafka is not in the byte path.

**Why the play-latency problem ("video sometimes needs close+reload
before playing") has a different root cause:**

Almost certainly one or more of:

1. **Video not encoded with `-movflags +faststart`**: MP4's index (moov
   atom) defaults to the END of the file. Browsers can't start playing
   until they've downloaded the whole file. `-movflags +faststart`
   moves the moov atom to the front and the browser starts playing
   after ~500 KB. Biggest single win.
2. **Slow presigned URL generation**: Python's boto3 signing is not
   fast; Go's `aws-sdk-go-v2` signing is significantly faster. Free
   win from the rebuild.
3. **First-byte latency from cold MinIO**: Garage has better cache
   locality by default.
4. **Redirect chain adds RTT**: `Cache-Control` on the 302 lets the
   browser cache the target URL and skip the API round-trip on repeat.

**What ships in the new stack to fix play latency:**

- **`-movflags +faststart` on every downloaded video.** The
  `DownloadVideo` activity (§7) invokes ffmpeg with this flag as part
  of its download/normalize pass. No exceptions.
- **Cache-Control on the 302 redirect** (`Cache-Control: public,
  max-age=300`) so the browser caches the S3 URL for 5 minutes and
  doesn't re-hit `/api/v1/videos/{share_id}` for repeated plays of
  the same clip.
- **Browser preload via SSE `event.video_ready`.** The
  vedanta-systems React component subscribes to the found-footy
  SSE stream and, on `event.video_ready`, sets the `<video src=...>`
  attribute IMMEDIATELY on a hidden video element (browser starts
  buffering). By the time the user clicks, the first frame is ready.
  Zero new backend infrastructure — reuses the NATS event we already
  publish.
- **Optional (Phase C follow-up)**: server-side pre-warm. When
  `event.video_ready` fires, `api` issues a small server-side GET
  against the presigned URL to warm Garage's cache before the user's
  browser asks. Deferred until we have real telemetry showing the
  browser preload isn't enough.

**Consequences:**

- Rebuild plan §7 (video pipeline) documents `-movflags +faststart` as
  a hard requirement of the `DownloadVideo` activity.
- Rebuild plan §8 SSE event catalog notes that `event.video_ready` is
  the load-bearing signal for frontend preload.
- vedanta-systems frontend gets a small change during Phase C cutover:
  the video-tile component subscribes to `event.video_ready` and
  preloads the `<video src>` when it fires.
- The play-latency bug is treated as a **known issue in Python prod
  that the Go rebuild resolves structurally**, not something to
  monkey-patch in Python. If it becomes an urgent user complaint
  before the rebuild lands, we can backport `-movflags +faststart`
  to Python `download_single_video` as a small standalone fix.

## 2026-07-01 — Workspace NATS as event bus (replaces Postgres LISTEN/NOTIFY)

**Decision:** found-footy's async event stream (SSE fan-out, webhook delivery,
cross-project events consumed by vedanta-systems) flows through workspace
NATS at [`~/workspace/nats/`](../../nats/), NOT through Postgres LISTEN/NOTIFY
as originally spec'd in earlier revisions of `docs/rebuild-plan.md` §8/§11.

**Why:** ecosystem-level decision — see workspace decisions at
`~/workspace/vedanta-dhobley/docs/decisions.md` 2026-07-01 entry for the
full rationale. NATS is designed as a broker; per-project brokers fight the
design center; cross-project consumption is trivial via a shared broker with
NATS account isolation.

**What stays vs what changes:**

- **Postgres `event_log` table stays** (per §3 schema). Still the durable
  audit trail. Every event is INSERTed into `event_log` for durability
  + backfill on SSE reconnect.
- **NATS is the fan-out mechanism.** Every event ALSO gets
  `nats.Publish(subject, payload)` for realtime delivery to subscribers.
- **SSE handler** (§8): subscribes to NATS `event.>` and `fixture.>`
  subjects on the found-footy account instead of `LISTEN`ing on a
  Postgres channel. Forwards to browser SSE consumers unchanged.
- **Webhook delivery worker** (§8): consumes NATS via a durable JetStream
  consumer with automatic replay on restart. Replaces the SQL `FOR UPDATE
  SKIP LOCKED` polling loop.

**Subject scheme (found-footy account):**

- `event.detected`, `event.stable`, `event.video_ready`,
  `event.rank_recalculated`, `event.removed`, `event.download_complete`
- `fixture.activated`, `fixture.completed`

Fully qualified via the found-footy NATS account:
`found-footy.event.video_ready` etc. — but subject strings within the
found-footy account use the short form.

**Rebuild plan impact:** §8/§9/§10/§11/§14 updated in the same commit as
this decision entry. `internal/infra/nats/` adapter added to §9;
`docker-compose.yml` no longer runs a per-project NATS in §10 (workspace
NATS is a dependency, not a project container).

---

## 2026-07-01 — Fresh rebuild in parallel, not incremental refactor

The rebuild happens as a **from-scratch build alongside the running
prod stack**, not as an in-place refactor of the existing code. Legacy
prod keeps serving traffic; new code stands up in parallel; cutover is
endpoint-by-endpoint until legacy has zero callers.

**Why**: [`design-audit.md`](./design-audit.md) surfaced structural
gaps (data-layer god-class, embedded-arrays-vs-shared-primitive, no
tests, no deploy gate, `_event_id` overloaded five ways) that are
easier to fix by building fresh than by refactoring in place. The user
built the current system while learning documentation-driven agentic
development; the rebuild is the moment to apply everything learned
since. See also [`design-audit.md`](./design-audit.md) §16
implementation order — the F-0..F-6 phases were incremental; this
rebuild subsumes them.

Codified in [`rebuild-plan.md`](./rebuild-plan.md).

---

## 2026-07-01 — Postgres over Mongo (rebuild-context reversal)

The rebuild uses Postgres for structured data. This **reverses**
[`design-audit.md`](./design-audit.md) §3's verdict ("keep Mongo"),
because the audit assumed *incremental refactor* of prod Mongo data.

**Why the framing change flipped it**: in a fresh-build context, the
backward-compat costs that made Mongo the pragmatic pick evaporate.
Postgres's native schema is stronger than Mongo's JSON Schema
validators (can't be disabled, can't run in "warn"), foreign keys
actually enforce relationships, transactions across rows are
first-class, and the extension ecosystem (`pgvector`, `pg_trgm`,
`TimescaleDB`, `pg_partman`) bolts on new capabilities without new
services. `$addToSet` idempotency becomes `INSERT ... ON CONFLICT DO
NOTHING`; embedded arrays become normalized tables with joins. In a
fresh build these are the correct shapes.

**When the audit still applies**: for the *legacy read-side compat*
layer (queries against pre-cutover `fixtures_completed` documents),
Mongo stays online read-only. New writes go to Postgres exclusively.

---

## 2026-07-01 — Garage over MinIO for blob storage

The rebuild uses Garage (Rust, ~50 MB, S3-compatible, filesystem-backed)
instead of MinIO for blob storage.

**Why**: MinIO Inc.'s "Aistor" rebrand has visibly de-featured the
community AGPL release (web console removed, replication features
behind commercial license, commit cadence slowed). Betting on
community MinIO long-term is a real risk. Garage is actively
maintained, low resource footprint, and its on-disk format is just
content-hashed files in a directory tree — no proprietary format,
`ls`-able, `rsync`-backupable. Per-project isolation (each project's
docker-compose runs its own Garage instance) preserves the workspace
pattern established by Temporal / Postgres / etc.

**Application impact**: S3-compatible API means `boto3` / `aioboto3`
client code is unchanged. Only `S3_ENDPOINT` in `.env` differs.

---

## 2026-07-01 — LLM endpoint abstracted; nexus swap is config-only

The application never knows which LLM inference server it's talking to.
All calls go through a client that reads `LLM_ENDPOINT_URL` from
environment. Model IDs are discovered from `/v1/models` at startup.
Request/response shapes conform to OpenAI's chat-completions API,
which llama.cpp (joi today) and nexus (est. end of 2026) both speak.

**Why**: nexus is under active development at `~/workspace/nexus/`
and will replace joi as found-footy's LLM endpoint when ready. The
switch must be a `.env` edit + container restart, not a code change.
This decision codifies the abstraction as a load-bearing invariant.

**Consequence for [`design-audit.md`](./design-audit.md) §6**: the
Track-1 workspace LLM gateway proposal is deferred pending nexus's
eventual API surface. If nexus provides concurrency/priority/routing
built-in, the gateway is redundant. If nexus doesn't, a thin
found-footy-side traffic shaper (not a workspace-wide gateway) may
be needed. Decide after nexus lands.

---

## 2026-06-30 — Cross-doc linking via markdown, no `[[wiki-links]]`

Docs reference each other via markdown `[text](./path.md)` syntax;
no Obsidian-style `[[]]` wiki-links anywhere in the project.

**Why**: markdown links work in every rendering context (GitHub,
terminal, IDE, Obsidian's graph view). `[[]]` is Obsidian-only —
GitHub renders it as literal non-clickable brackets, IDEs treat it as
plain text, and it's ambiguous when filenames collide across project
vaults. The "knowledge graph" effect comes from richly interconnecting
docs, not the syntax — markdown achieves the same graph in Obsidian.

Codified in global AGENTS.md § Cross-doc linking; project-side
reminders in [AGENTS.md](../AGENTS.md) § Documentation and docstrings
and [docs/README.md](./README.md) intake rules.

---

## 2026-06-30 — Brain-stack (Khoj + basic-memory MCP + Obsidian vault) deprecated

The `~/workspace/obsidian/` self-hosted second-brain stack was deleted
on 2026-06-30. Never had containers running in practice, the vault was
empty of notes, the only configured project symlink was legal-tender's
`docs/`.

**Why deprecated**: the conceptual goal (cross-project knowledge
access via interlinked markdown) is now achieved by
`~/workspace/vedanta-dhobley/` as the user-global hub plus each
project's `docs/` with rich markdown cross-linking — no separate
aggregation layer or MCP-write-to-vault tooling required.

Global AGENTS.md was cleaned of the stale Khoj / basic-memory MCP /
vault references the same day; legal-tender's six file references
queued for cleanup in `~/workspace/vedanta-dhobley/docs/todo.md`.

---

## 2026-05 — Caddy fronts all HTTP; host ports dropped

HTTP services (Temporal UI, Mongo UI, MinIO console, Twitter VNC) moved
from publishing host ports to being reached through the workspace `proxy`
Caddy via per-project hostnames. Per-project Caddyfile lives in
`~/workspace/proxy/caddy/caddy.d/found-footy.caddy`. Only host port kept
is dev Temporal gRPC `7233` for host-side dev clients.

**Why**: avoids port-allocation thrash across the ~5 personal projects on
the node, makes per-project URLs memorable
(`found-footy-prod-temporal-ui.<base-domain>` beats `localhost:3200`), and
unifies access path with how `joi` already serves `llama-small.joi`.

See: `deploy/INFRA-NOTES.md`, commits `c71dbe3` + `c9d1c77`.

---

## 2026-XX — LLM URL switched to Caddy hostname on joi

`LLAMA_URL` moved from `http://joi.<tailnet>.ts.net:3102` to
`http://llama-small.joi` (no port). joi runs its own Caddy that maps the
model-named subdomain to whatever llama.cpp port that model is currently on.

**Why**: stable URL across model swaps and port reassignments. Project
code no longer needs to know which port a given model uses.

---

## (pre-history) — Scoped deduplication by `timestamp_verified`

In `UploadWorkflow`, incoming videos are split by `timestamp_verified`
before perceptual-hash comparison: verified pool compares only against
verified S3 videos, unverified against unverified, both via parallel
`asyncio.gather()`.

**Why**: a verified goal clip and an unverified clip from a different
match moment can hash-match (same broadcast, same camera angles). Without
scoping, the verified clip could be replaced by the unverified one. In
prod this correctly blocked a Goal 1 clip from being replaced by a Goal 2
clip in the same match (expected ~31′, got 15′).

---

## (pre-history) — Workflow-ID arrays over counters

`_monitor_workflows`, `_download_workflows`, `_drop_workflows` are arrays
of workflow IDs, mutated via MongoDB `$addToSet`. The previous design
used plain integer counters incremented after work completed.

**Why**: idempotent (re-running the same workflow can't double-count),
auditable (you can see which workflows participated), failure-resistant
(a failed workflow simply doesn't register, so counts naturally stay low
without manual decrement).

---

## (pre-history) — `signal-with-start` for serialized `UploadWorkflow`

Each event has exactly one `UploadWorkflow` with deterministic ID
`upload-{event_id}`. `DownloadWorkflow`s queue videos onto it via
`signal-with-start`. The upload workflow processes signals FIFO and exits
after 5 min of idle.

**Why**: multiple parallel downloads completing simultaneously would race
on S3 dedup and produce duplicate uploads. Serializing per event
eliminates the race; deterministic ID + signal-with-start handles "is
there one already running?" without an explicit check.

---

## (pre-history) — Twitter alias resolution inside `TwitterWorkflow`

Originally there was a separate `RAGWorkflow` that resolved aliases then
fire-and-forgot into `TwitterWorkflow`. That double fire-and-forget chain
produced duplicate Twitter workflows when either side retried. Now
`TwitterWorkflow` does the cache lookup (or RAG fallback) inline at its
own startup.

**Why**: one workflow, one set of retry semantics, no orphan duplicates.
The `RAGWorkflow` is still around but only for pre-caching during
`IngestWorkflow`.

---

## (pre-history) — 5-collection MongoDB design with `fixtures_live` as overwrite buffer

`fixtures_staging` (upcoming) → `fixtures_active` (in-progress, enhanced
with our `_*` fields) → `fixtures_completed` (archive, 14-day retention).
`fixtures_live` is a temporary overwrite buffer for raw API data so we can
diff against the enhanced events in `fixtures_active` without destroying
our enhancements. `team_aliases` is a persistent RAG cache.

**Why**: the earlier single-collection-merge approach kept overwriting our
enhancement fields (`_monitor_workflows`, `_s3_videos`, etc.). The
5-collection design treats "raw API state" and "our enhanced state" as
two separate documents that we only ever compare, never merge in place.

---

## (pre-history) — Auto-scaling via dedicated scaler container

`docker compose up -d` brings up infrastructure + a single scaler service.
`worker` and `twitter` are marked `profiles: ["managed"]` so they aren't
started by default. The scaler reads Temporal queue depth + the MongoDB
active-goal count every 30 s and uses `docker compose up --scale` to
adjust replicas between 2 and 8.

**Why**: Python's GIL limits each worker to one CPU core; the workload is
spiky (zero goals one minute, eight simultaneous the next). Hand-tuning
replica count doesn't work — the scaler reacts in seconds.

---

## (pre-history) — Fire-and-forget child workflows with `ABANDON` parent close policy

`MonitorWorkflow → TwitterWorkflow` and `TwitterWorkflow → DownloadWorkflow`
both use `start_child_workflow(... parent_close_policy=ParentClosePolicy.ABANDON)`.

**Why**: parent workflows have short cycles (Monitor is every 30 s, Twitter
loops ~10 min) — child workflows must outlive them. ABANDON means parents
can complete and be GC'd while children continue independently. Without
this, children would be cancelled when parents exit and we'd lose work.

---

## (pre-history) — Heartbeat-based timeouts for long-running activities

Activities like `generate_video_hash` and `deduplicate_videos` use
`heartbeat_timeout` instead of (or in addition to) `start_to_close_timeout`.
They emit `activity.heartbeat(...)` between units of work — e.g., every 5
frames during hash generation, every video during S3 comparison.

**Why**: arbitrary execution timeouts kill long-but-progressing activities
on big videos. Heartbeats prove the activity is actually making progress —
Temporal only fails it if heartbeats stop, not if total time exceeds an
arbitrary deadline.
