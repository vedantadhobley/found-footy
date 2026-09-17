# Provider health evidence

**Atomic boundary committed as `89d1d7c`; shadow-mode follow-up implemented locally;
production schema prepared, application deployment pending.** This is the durable
evidence and atomic write-protection contract for
[FF-075](./todo.md#ff-075--successful-provider-responses-can-destructively-regress-live-state).
Production still uses the earlier stateless shadow evaluator until an approved
application deployment. The
[design proposal](./design/proposals/provider-integrity-circuit-breaker.md)
owns the remaining operator-resolution, correction-evidence, telemetry, and
search-recovery work.

## Polling path

New `ActivePollWorkflow` histories use `provider-health-evidence-v1` and
`provider-mutation-guard-v1` markers:

1. Fetch the existing active-fixture ID batch. Keep the current cadence and
   activity retry policy. A failed attempt writes bounded coverage/class/reason
   evidence before returning its retryable error.
2. Run `CollectProviderHealth` before reconciliation, including total fetch
   failure and empty-result paths. Use a deterministic digest of the workflow
   ID/run pair plus immutable cycle start time as logical observation identity.
3. Read canonical fixture/history facts and reuse the reconciliation event
   sequence matcher. Do not create a second matching algorithm.
4. Under the provider policy lock, verify that each usable comparison still
   describes the canonical `last_polled_at` baseline. Commit typed progress
   cursors, unresolved causes, provider correlation/recovery, and the finalized
   receipt in one evidence-only transaction. A changed baseline aborts the poll.
5. Build one immutable reconciliation plan per returned fixture. Under the same
   provider policy and per-fixture locks, require the exact health receipt and
   generation, then commit every permitted fixture field, event operation,
   debounce vote, required audit, downstream checklist row, and completion in
   one transaction.
6. Start Temporal work, publish NATS invalidations, and reclaim media only after
   the guarded transaction returns its durable result.

New staging-poll and ingest histories use the same mutation boundary without an
active-poll health receipt: they read the current provider/fixture policy under
lock and combine it with a local stored-versus-observed assessment. Date-list
ingest responses do not treat missing event arrays as empty inventories. Old
histories keep their recorded activity sequence through the same version marker.
Empty active coverage does not fabricate a healthy observation. No extra
provider request, background service, Temporal schedule, frontend field, or
NATS subject is added.

## Durable state

The two ordered migrations add four internal tables without rewriting existing
fixture/event rows:

| Table | Responsibility |
|---|---|
| `provider_health` | One API-Football observation cursor, recommended provider policy/generation, recent distinct-fixture onsets and bounded recovery probes. |
| `provider_fixture_health` | Per-fixture observation/usable/progress/phase timestamps, observed phase, clock high-water mark, and versioned unresolved-cause evidence. |
| `provider_health_polls` | Logical poll receipts, failed fetch attempts, compact results and cause transitions. No complete provider payload per poll. |
| `provider_mutation_receipts` | Per-observation/per-fixture committed results, including rollout mode, policy inputs/recommendation, limited plan operations, applied policy and post-commit effects. Retries cannot repeat a vote or lose required cleanup. |

Progress fields use typed SQL columns. Cause-specific baselines and bounded
probe windows use versioned JSON. SQL constrains identity, cursor ordering,
enums, versions, and payload size. Unknown evidence versions fail collection
rather than silently resetting trust history.

Health poll and mutation receipts are retained for fourteen elapsed days, in
small indexed batches. This is **not** the public fourteen-fixture-date retention
rule. Unresolved causes never expire with their receipts; fixture health follows
retained fixture identity through a foreign key. Historical state rows are not
assertions about currently monitored coverage.

Health never writes `event_log`, `last_activity_at`, public fixture fields, event
votes, candidate state, or media. Receipt transitions carry bounded before/after
facts and missing confirmed keys when relevant. Clock/fetch incidents retain
their pre-gap progress/usable timestamp for future recovery-window work.

## Evidence rules

- A running interior clock needs three minutes of continuous observations
  without advancement to raise a stalled cause. Period boundaries need ten
  minutes and raise warnings only. Pauses and unknown clocks remain distinct.
- Identical successful responses, metadata changes, and rollback/return
  oscillations cannot reset the clock high-water mark.
- A failed observation, failed attempt before success, phase change, or gap
  longer than ninety seconds resets continuity. Retained incidents remain open.
- Availability recovers on usable data. Freshness needs two clock advances or
  a forward phase plus two compatible later observations. A retry never counts
  as another logical poll.
- Each integrity reason retains its original fixture and event comparison
  baseline. Later canonical changes cannot erase that contradiction or use an
  unrelated recent cancellation to explain an older missing goal.
- Stored event evidence has two scopes. Every active generation, including one
  still debouncing, can prove a recent score-coherent goal cancellation and
  receive its normal absence vote. Only stable generations contribute missing-
  event outage signals and provider-wide correlation.
- Two distinct newly affected fixtures within two minutes can correlate.
  Repeated old causes and multiple missing events in one fixture cannot create
  provider-wide scope. Multiple stalls warn at provider scope but recommend
  restrictions only for those fixtures.
- Provider integrity recovers after three complete distinct polls with no new
  affected fixture/systemic anomaly. Unresolved fixture causes stay independent.

These thresholds are initial policy choices, not guarantees of real-world
freshness or complete goal coverage. The recent probe ring is bounded; very
high-frequency manual polls can conservatively delay recovery.

## Retries, failures, and concurrency

The provider row serializes health commits. Guarded writers take a compatible
`FOR SHARE` lock, then a fixture-scoped advisory lock and fixture row lock.
Different fixtures can still commit concurrently; a newer health generation
cannot pass a writer in flight. No database lock is held during HTTP, planning
reads, Temporal, NATS, or Garage calls.

Health comparisons are necessarily read before the evidence transaction. After
the provider lock is acquired, the collector compares every usable fixture's
captured `last_polled_at` with the locked canonical row. Any mismatch returns
`ErrStaleMutationPlan`, rolls back the complete evidence receipt, and lets the
next scheduled poll rebuild from current state. Active mutation plans perform
the inverse checks: exact health poll ID/time, generation, fixture lifecycle,
and baseline observation must all still match.

A committed poll receipt returns the same result on activity retry. Older/equal
observation versions cannot replace newer evidence, even after receipt pruning.
Failed-attempt writes are idempotent by logical poll and attempt number. A
successful retry preserves those failures and interrupts the recovery sequence.

Provider transport/contract failures remain separate from local history-read,
matching, unclassified fetch, or newer-canonical-snapshot failures. Local
uncertainty is recorded as `unobserved`, not proof of a vendor outage. A health
collection failure closes the active write gate for that poll; the next
scheduled poll retries from fresh provider and canonical state.

Committed retry receipts return the original effect list, so activity response
loss cannot double-cast a debounce vote or omit a required downstream spawn,
removal cleanup, or publication. Fixture status versus structural notification
classification is stored in that receipt; retries must not recompute it against
the already-updated fixture. A process loss before a failed-attempt write
cannot be reconstructed as a known provider failure; subsequent observation
gaps remain unknown evidence.

## Shadow and enforcement modes

Worker-only `PROVIDER_INTEGRITY_MODE` defaults to `shadow`. All active, staging,
and ingest writers receive the same setting. `enforce` is opt-in; other values
fail startup. No Compose or production environment edit is required to obtain
the shadow default in a new build.

Both modes evaluate the same health policy under the same transaction locks.
Shadow applies ordinary `trusted` semantic permissions but retains the complete
recommendation. Enforce applies that recommendation. Neither mode disables
wire validation, stale/baseline checks, event ownership, atomic commits, retry
receipts, or existing score-backed absence protection. A collection failure
still stops that active poll; shadow is not a bypass for inconsistent writes.

The mutation receipt's `Policy` is the **applied** policy. `Decision` records
`Writer`, `Mode`, `ProviderPolicy`, `FixturePolicy`, `LocalPolicy`, and
`RecommendedPolicy`. `Decision.Restricted` identifies the limited fixture
refresh, event inserts/presence votes, event corrections, absence votes,
placeholder deletes, and elapsed-grace completion check in that plan.
Fixture refreshes and event corrections can be partially allowed; these are
operation scopes, not a field-level alternate result. An absence vote does not
necessarily remove an event. A held completion check does not prove that no
debounce or downstream work remained. Actual committed effects remain separate
in the receipt. Trusted recommendations have no restricted-operation payload.

Stale plans never evaluate policy and have no decision. Old receipts also lack
that field. A retry preserves the original result/mode, even when the current
worker runs a different mode. Cause openings and recoveries remain in health
poll receipts and do not reset on mode changes. Existing JSON receipt columns
carry the added evidence; there is no additional schema migration.

See the [shadow rollout decision](./decisions/2026-09-17-provider-policy-starts-in-shadow.md).

## Mutation policy semantics

Every guarded writer composes provider, fixture, and local observation policy;
the most restrictive result wins:

- `trusted` applies normal reconciliation, including supported destructive
  corrections and fixture completion.
- `positive_only` advances clocks/phases when credible, preserves monotonic
  scores/penalties and existing populated facts, inserts new events, accepts
  presence votes, and fills missing assist data. It suppresses absence votes,
  anonymous-placeholder deletion, regressive mutable event fields, terminal
  observation changes, and completion.
- `rejected` performs no provider-derived canonical mutation.

Restricted merges retain an omitted clock. Winner flags still derive from the
retained score and penalty values: an accepted equalizer clears the old leader.
These derived flags are not themselves monotonic source facts.

The guarded transaction does not perform external effects. It records required
audits and downstream checklist rows with the state change, returns the durable
effect set, and lets the activity perform recoverable external work afterward.
Existing discovery recovery remains available even when the current provider
observation is rejected because that work derives from already committed state.

## Diagnostics and remaining work

`ActivePollWorkflowOutput.ProviderHealth` contains current coverage, recommended
policy/generation, affected IDs, correlation, and transitions. Its reconciliation
summary reports applied `PositiveOnlyWrites` / `RejectedWrites`, separate
`ShadowPositiveOnly` / `ShadowRejected` recommendation counts, and stale plans.
These count fixture observations, not goals saved or lost. Log cause
transitions, not a repeated stalled warning every thirty seconds. Existing PG
query metrics cover storage calls. Dedicated provider-health Prometheus
instruments remain pending.

The old `ProviderIntegrity` output remains the stateless local reconciliation
diagnostic. Its aggregate heuristic is **not** the durable provider policy; the
per-fixture verdict is one input to the recommended policy. Removing the aggregate
diagnostic is optional cleanup after rollout evidence proves it no longer pays
for itself.

Raw correction evidence, audited operator restriction/resolution, incident-linked
search-window initialization, and dedicated metrics remain separate work. The
collector and reconciler still repeat canonical fixture/history reads. Measure
their cost after rollout before optimizing; sharing a plan across activity
boundaries would add more contract surface than it removes today.

Existing event-generation semantics remain unchanged: a confirmed event removed
before this protection can later return under another key. That does not
automatically resolve the old missing-key cause. The recorder retains the
conflict rather than inventing an identity match or altering old events. A
future explicit recovery rule or audited operator decision must close it.

Search windows, query text, ranking, and terminal grace are unchanged. Run at
least one complete match window in shadow, then review restrictions and recovery
before enabling enforcement. Quiet polls alone do not validate anomaly handling.
Shadow evidence is not a full counterfactual replay: enforcement would produce
different canonical state. Original incident baselines survive shadow writes.

For a bounded window, inspect recommendation versus actual mode/policy:

```sql
SELECT result->'Decision'->>'Writer' AS writer,
       result->'Decision'->>'Mode' AS mode,
       result->'Decision'->>'RecommendedPolicy' AS recommended,
       result->>'Policy' AS applied, count(*) AS observations
FROM provider_mutation_receipts
WHERE observed_at >= now() - interval '24 hours' AND result ? 'Decision'
GROUP BY 1, 2, 3, 4 ORDER BY 1, 2, 3, 4;
```

Review restricted receipts by fixture/observation against their actual effect
fields and `provider_health_polls.result.Transitions`. Check normal period
changes, corrections, completion, isolated/provider-wide scope, and recovery
delay. Never infer lost goals by summing restricted-operation arrays.

## Verification checkpoint — September 16

Focused domain, Monitor, ingest, workflow, and Postgres short suites pass. The
disposable-Postgres suite proves:

- an August-style 4–2/65′ to 0–0/NS response preserves canonical score/status
  and casts no absence vote while a newly observed event still lands;
- a late invalid audit rolls the complete fixture/event plan back;
- daily ingest cannot bypass a fixture-local restriction;
- staging activation and its audit commit once and return the same result after
  simulated activity-response loss;
- committed score/grouping and clock/halftime notifications survive replanning
  after simulated activity-response loss;
- a canonical write between health comparison and evidence commit aborts the
  whole health poll;
- restart-independent health state, failed-attempt receipts, retention, ordered
  migrations, and current-schema adoption remain valid.

The scenario corpus runs the guarded composition end to end. Its downstream
fake may close only a checklist row that the atomic mutation already created.
It preserves score-backed VAR decay for pending and stable goals, freezes
unsupported disappearance, delays terminal grace until coherent evidence
returns, and still completes fixtures after downstream work settles.

The full scoped repository command passed before the final review corrections;
the affected domain, activity, workflow, PostgreSQL guard, and scenario suites
were rerun successfully afterward. Scoped vet, pinned lint, module-tidy, and
maintained-root formatting checks passed. The full scoped command was:

```sh
go test -buildvcs=false -p=2 ./cmd/... ./internal/... ./migrations/... ./scripts/... ./test/...
```

The exact staged snapshot subsequently passed the complete unchanged
`make check` gate from a clean temporary export, including all PostgreSQL and
scenario tests. Only Go package parallelism was capped at two. The normal
pre-commit hook remains enabled. In the original working directory, bare
`make test` includes existing gitignored research programs: duplicate `main`
functions in `scratch-audit-2026-09-14` prevent that package from compiling.
Those files, the Makefile, and hooks were left untouched. The older direct-support
migration test was repaired to rewind the full migration suffix, not leave a
ledger gap when a later migration exists.

At that local verification checkpoint, no production changes had been performed.

## Shadow-mode verification — September 17 UTC

All maintained Go-package short tests, focused race tests, pinned scoped lint,
the disposable-Postgres provider mutation suite, and the scenario suite pass.
The shadow tests cover positive-only/rejected recommendations across all three
writers, terminal completion, stale-plan protection, and original receipt
effects across a mode change. A durable recovery test applies the bad score in
shadow, confirms the next identical response remains restricted against the
original baseline, then restores that baseline and verifies the cause resolves.

The shadow follow-up is implemented; application deployment remains pending. It does not modify any
migration, production configuration, or production data. The earlier complete
`make check` checkpoint above belongs to `89d1d7c`, not this follow-up.

## Production schema checkpoint — September 17 UTC

With explicit migration approval, the unchanged `make migrate-prod` command
built commit `89d1d7c39cfb888ec357387c31ad1cee7672a5db` from its clean tested
snapshot and applied both September 16 migrations at `2026-09-17 01:24:51 UTC`.
Preflight verified every existing migration checksum and exactly two pending
files. The runner reported `migration verified` for that commit; an independent
read confirmed both ledger rows, all four tables, and schema hash
`8a9ba0bebf596112f1032bc5b9a892f64bec9e1abc8cebd1ed9b1bb9e0ac12e2`.

Only the provider singleton is seeded. No health observations or mutation
receipts exist yet. Worker/API containers remained on `8c836e0`, with unchanged
start times, and polling continued after the migration. No application restart,
deployment, push, fixture/event rewrite, or media mutation was performed.

The new collector and transaction boundary are **not active** until a separately
approved application rollout. That rollout will default to shadow; semantic
restrictions require a later explicit enforcement change. Existing processes
can continue using the additive schema, but an old
binary restarted against the newer migration ledger fails its startup check;
deploy the compatible application before any such restart. Do not delete
migration rows to bypass that check.
