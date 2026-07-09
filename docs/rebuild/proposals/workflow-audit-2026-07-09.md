# Ingest + Monitor Workflow Audit — 2026-07-09

Cross-referenced current Go IngestWorkflow + MonitorWorkflow implementations
against Python archive + `docs/rebuild-plan.md` §5 W1/W2 + decisions.md
entries. Punch list at top, per-workflow details below.

**Method**: two parallel `Explore` subagents, one per workflow. Each
categorized findings as **MISMATCH** (real bug — code disagrees with
Python/plan on load-bearing behavior), **GAP** (Python/plan has a
feature Go doesn't), or **SAFE_DIVERGENCE** (intentional Go improvement
or acceptable difference).

**Nothing was changed.** All items are proposals.

**Load-bearing takeaway**: **zero real bugs**. Ingest is essentially
complete (some documented follow-ups). Monitor has three known
deferrals that block O2 completion — all called out in the workflow's
own docstring, no surprises.

---

## Punch list — ranked

### P0 — blocks O2 completion (Monitor gaps only)

1. **15-min staging poll** — designed 2026-07-07, config landed 2026-07-09,
   behavior NOT SHIPPED. Currently staging fixtures get no API poll until
   pre-activation. Postponements + kickoff changes on staging fixtures
   go undetected (the exact bug that surfaced in Python's 2026-07-05
   Mexico vs England postponement). Monitor's own docstring calls this
   out.
   - Files: [`internal/activity/monitor/activities.go`](../../../internal/activity/monitor/activities.go) `PreActivateUpcoming` (line ~113) has DB-only logic; needs sibling `PollStagingFixtures` activity that runs on 15-min aligned boundaries.
   - Est. scope: ~150 lines + tests, ~1 commit.

2. **Fixture completion detection** — no code marks fixtures `completed`.
   Fixtures stay in `active` state forever. Python's `complete_fixture_if_ready`
   (`archive/src/activities/monitor.py:367-483`) checks per-event
   `_monitor_complete + _download_complete` before promoting to
   completed. Frontend doesn't know when matches end.
   - Files: needs new activity + workflow step. Depends on Discovery/Download
     pipeline (O3) being wired so events can reach terminal state.
   - Est. scope: partial now (mark completed when API status is Terminal
     with no unresolved events), full only after O3.

3. **Semantic-event emissions (NATS) + Discovery spawn** — plan §5 W2
   line 3339-3364 says Monitor emits `fixture.activated`, `event.detected`,
   `event.stable`, `event.removed`, `fixture.completed` as NATS messages;
   Discovery subscriber goroutine spawns `DiscoveryWorkflow`. Go currently
   just logs counts in `MonitorWorkflowOutput`. Everything downstream
   (Twitter search, video validation, S3 upload) is blocked until this
   lands.
   - Files: new activity `EmitSemanticEvent`, wiring in `ReconcileFixture`,
     NATS subscriber goroutine somewhere. Overlaps with O3 phase planning.
   - Est. scope: significant — this is the O3 kickoff.

### P1 — worth doing sooner rather than later

4. **Emergency activation on APIStatus.Live()** — decisions.md 2026-07-07
   activation triggers §3: if a staging fixture's API response comes back
   showing `Live()`, promote immediately (already-live case where ingest
   had the wrong kickoff or the vendor published a corrected one). Python
   has this (`activities/monitor.py:243-251`). Go doesn't.
   - Only observable after #1 (staging poll) ships. Cheap to add at the
     same time.

5. **Tournament coverage requires config edits** — Go filters fixtures
   by tracked team IDs, and tracked teams come from `/teams?league=X`
   calls per `TrackedLeagueIDs` env. WC (league 1) is in default env
   → 32 WC teams caught. But Euros, Copa America, Nations League etc.
   need their league IDs added to the env when they run. Python's
   `TOP_FIFA_IDS` (15 static national team IDs) catches them
   transparently regardless of tournament.
   - Options: (a) add a static `TOP_NATIONAL_TEAMS` const + always
     union into tracked set (matches Python), (b) automate tournament
     detection (hard — the vendor has no "current tournaments" endpoint),
     (c) accept the manual step + document it in the run book. User
     already flagged general national-team tracking as an open design
     conversation.

6. **Static UEFA fallback for total team-fetch failure** — Python has
   `TOP_UEFA_IDS = [15 clubs]` fallback if the API-driven top-flight
   fetch fails completely. Go currently returns error → keeps previous
   cache (which may be empty on first-ever run) → fail-open (no filter).
   - Fail-open is broader-scoped than Python's static list. Not necessarily
     worse — first-ever failures are extremely rare and quickly self-heal
     on next Ingest cycle.

### P2 — polish

7. **Alias metadata loss at ingest** — Python passes `country` from
   `league.country` field into RAG. Go's `EnsureAliasPlaceholders` stores
   only `TeamID` + `TeamName`. Minor — RAG can still infer country from
   the team API, just costs one extra API call.

8. **Ingest retry constants could move to config** —
   `ingestManualIDsMaxAttempts=3`, `ingestManualIDsBackoffInitial=5s`
   currently hardcoded. Workflow-scoped so not a drift risk, but for
   consistency with the just-landed WorkflowsConfig work these could
   move too.

9. **Removal-reason hardcoded to 'var'** —
   `internal/infra/pg/event_repo.go:372` hardcodes `'var'` when
   registering an event absence. Domain supports three reasons
   (`RemovalVAR`, `RemovalPolicy`, `RemovalAssetGone`), but only VAR
   is ever written. Fine today since VAR is the only load-bearing
   case, but the code shape suggests it should be pluggable.

### Not-a-bug — Go IS more correct than Python

- **Ingest-time pre-activation** — Go promotes imminent fixtures
  (kickoff within `ActivationWindow`) straight to `active` at
  Categorize time. Python doesn't. Fixes Python's known "ingest
  at 14:55 for 15:00 kickoff sits in staging" bug. Documented in
  decisions.md 2026-07-07.
- **Deferred RAG** — Go writes placeholder alias rows during ingest;
  Python does synchronous per-team RAG (Wikidata + LLM) inline.
  Python's approach ties ingest completion to LLM availability;
  Go's decouples cleanly.
- **Symmetric-counter debounce** — Go's single counter (0↔1↔2↔3, ±1)
  vs Python's asymmetric (monotonic `_monitor_workflows` + reset-on-
  presence `_drop_workflows`). Simpler to reason about, user-approved
  per decisions.md 2026-07-07.

---

## Aggregate counts

| Category | Ingest | Monitor | Total |
|---|---|---|---|
| MISMATCH (real bugs) | 0 | 0 | **0** |
| GAP — blocking | 0 | 3 | 3 |
| GAP — worth doing | 2 | 1 | 3 |
| GAP — polish | 3 | 0 | 3 |
| SAFE_DIVERGENCE (Go improvement) | 5 | 3 | 8 |
| MATCH | 3 | 4 | 7 |

**Ingest**: essentially done. Zero blockers, three documented follow-ups.

**Monitor**: three known deferrals (staging poll, completion, NATS
emissions) block O2 completion. All flagged in monitor.go's own
docstring. No surprises.

---

## Per-workflow details

### IngestWorkflow

**Full audit**: Explore agent output (see task-notification transcript).

**Fetch strategy** (per-day `date=` + smart lookahead): MATCH with
intentional enhancement. `FetchFuture` flag adds optional lookahead
for scheduled path; manual triggers default to anchor-day-only for
surgical re-ingests.

**Team filtering** (GAP): Go uses config-driven tracked-league team
roster fetch. Python combines dynamic top-flight + static
`TOP_FIFA_IDS`. Two consequences:

- Go needs tournament league IDs in env to catch international
  fixtures. WC=1 is in default env. Others (Euros, Copa) require
  manual env edit.
- No static UEFA fallback if refresh fails completely. Failure mode
  is fail-open (return everything unfiltered) rather than
  fall-back-to-15-hardcoded-teams.

**Categorize** (MISMATCH, but Go is more correct): Python skips
already-existing fixtures. Go upserts (refreshes API fields, preserves
domain state). Same outcome, Go's is more operationally useful. Go
also pre-activates imminent fixtures at ingest time — Python doesn't.

**RAG** (SAFE_DIVERGENCE + minor GAP): Go defers RAG to separate batch
job. Placeholder rows only. Loses country/city hints Python passes.

**Retention** (SAFE_DIVERGENCE): No S3 cleanup path in Go — videos
live in `video_assets` pg table, not S3. Schema-driven difference,
not regression.

**Frontend notification** (SAFE_DIVERGENCE): Go defers to NATS
wire-up per plan §5 W1. Correct alignment.

**Manual triggers** (SAFE_DIVERGENCE): Go adds `FetchFuture`, override
flags for `ActivationWindow` + `RetentionDays`. More flexible than
Python.

**Config** (MATCH): WorkflowsConfig centralization matches design
intent. A few workflow-local constants remain hardcoded (retry policy
for manual-IDs path) — polish item.

**Retry policies** (MATCH): Exactly matches rebuild plan spec.

### MonitorWorkflow

**Full audit**: Explore agent output (see task-notification transcript).

**Cycle interval** (MATCH): 30s, now config-driven via
`ActiveFixturePollInterval`.

**Active fixture polling** (MATCH): Batch by-IDs with client-side
chunking + partial-failure `FailedIDs`. Per-fixture parallel
reconcile via `workflow.ExecuteActivity` loop + Future.Get. Same
outcome as Python's `asyncio.gather`.

**Pre-activation** (partial GAP): DB-only `PreActivateUpcoming`
implemented. Emergency activation on `APIStatus.Live()` NOT implemented.

**15-min staging poll** (BLOCKING GAP): Designed, configured, not shipped.

**Event debounce** (SAFE_DIVERGENCE): Symmetric counter is defensible
improvement over Python's asymmetric model. Both detect NEW /
INCOMPLETE / REMOVED / VAR overturn / flicker.

**Downstream triggers** (BLOCKING GAP): No NATS emissions, no
DiscoveryWorkflow spawn. Everything downstream blocked until this
lands. This is the O3 kickoff.

**Fixture completion** (BLOCKING GAP): No completion detection.
Fixtures stay `active` forever. Partial fix possible today (mark
Terminal API status → complete when no unresolved events); full
solution needs O3 pipeline.

**Removal reason** (polish GAP): Domain supports 3 reasons but
`event_repo.RegisterEventAbsence` hardcodes `'var'`.

**Concurrency** (MATCH): `workflow.ExecuteActivity` in a loop achieves
true parallelism — matches Python's `asyncio.gather` pattern.

**Config** (MATCH): All values threaded through
`config.Workflows` correctly. `StagingPollInterval` field exists but
not yet consumed (staging poll not shipped).

---

## What to do next

The user should decide priorities. Suggested sequence:

1. **Ship the 15-min staging poll** (punch list #1). Config is already
   there, activity + workflow step is ~150 lines. Standalone — doesn't
   depend on O3.
2. **Add emergency activation** (punch list #4). ~10 lines added to
   `PreActivateUpcoming` after #1 lands.
3. **Design conversation on national-team coverage** (punch list #5).
   User-flagged as an open design item; decide static-vs-dynamic
   before shipping.
4. **Kick off O3** — NATS emissions + Discovery spawn (punch list #3).
   Bigger phase; separate planning conversation warranted.
5. **Polish items** (#7-#9) — batch commit when convenient.

Ingest is done enough to leave alone until we start using it in
anger. Monitor's blockers are the load-bearing ones now.
