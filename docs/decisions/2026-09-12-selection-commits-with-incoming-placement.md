# Selection commits with incoming placement

## Status

Implemented locally for FF-081, not deployed. This integrates the
[direct-support planner](./2026-09-12-restored-clips-keep-one-source-owner.md)
with the existing accepted-candidate activity. It does not change the quality
comparator, dHash thresholds, clock acceptance or FF-078 visibility policy.

The subsequent [direct-support decision](./2026-09-13-popularity-counts-direct-support.md)
changes public scoring within this same transaction. Scores now overlap across
keepers; canonical routing remains exclusive. Source identities stay unique,
but summed selected popularity is no longer a conserved source total.

## One durable operation

New histories pass a selection operation ID and recorded matcher policy through
`CommitClipPlacement`. The ID derives from the workflow run and primary source
URL; an activity retry carries the same input. The activity reads a repeatable
selection snapshot before media preparation. It copies incoming bytes when
needed and checks eligible retained objects with the existing S3 `Head` operation,
bounded to four concurrent checks. Missing hidden media is excluded; a failed
HEAD retries rather than being interpreted as absence. A newly retained variant
must have its own accepted validation and present bytes, even when it loses.

The existing event-locked PostgreSQL transaction now composes ordinary placement
with direct-support reselection. It checks the pre-preparation snapshot before
mutating anything, retains the incoming candidate/asset/evaluation, plans against
the resulting topology, and updates shares, aliases, source owners and popularity
before commit. No intermediate keeper set becomes visible.

The existing `video_selection_commits` table records the pre-placement topology,
matcher policy, placement result and selection result. Integration uses the
accepted-validation and selection-receipt migrations; no additional store is
needed. Review added an ordered
[index-only migration](../../migrations/20260912_03_index_event_validation_lookup.sql)
for `(event_id, asset_id, recorded_at, id)` on accepted validations. Every snapshot
can read one event's earliest evaluations without scanning unrelated retained
history. The asset-leading index remains useful for asset-local history and
foreign-key deletion checks. Neither index contains the evidence JSON itself.

## Retry and recovery

A receipt binds its ID to the accepted input and policy. Preparation snapshots,
prepared-object lists and activity wall time are excluded from this binding
because a retry may legitimately observe later state. Reusing the ID with
different accepted evidence or policy fails. The receipt check precedes the old
placement, whose winner/loser pointers may no longer be current.

After commit and staging cleanup, the activity loads current state from one
consistent snapshot. It returns current roots, popularity and exact aliases—not
the historical receipt's selection. The workflow replaces both caches before
another candidate or `event.update`. Surviving incumbents keep their comparison
order, followed by an incoming winner and restorations in planner order; extra
current roots seen on retry use recovery's evidence order. Exact recurrence uses
the returned source count, without an additional in-memory increment.

Stale uncommitted inputs retry without partial SQL changes. If another execution
made the fixed incoming decision invalid, normal failed-run recovery recomputes
it from current durable state. Existing completion and notification contracts
remain: placement publishes after its durable tail, and completion publishes
after checklist closure. No new NATS subject or frontend change is needed.

## Incomplete history

The pure planner still refuses missing or inconsistent source attribution.
Within incoming placement, only that explicit `ErrSelectionCredits` result skips
the graph repair. Ordinary placement commits with `Skipped=incomplete_credits`
in its receipt and a bounded workflow warning. It does not invent a split, and
does not strand a newly accepted source because of an old attribution gap.
Malformed topology, stale state, missing required media or invalid new evidence
remain errors; they are not silently downgraded to the legacy path.

## Removal and retention

Share revocation now takes the same event row lock before its UPDATE. Thus it
cannot miss a new or restored share inserted concurrently. Revocation still
precedes object deletion; the selection transaction refuses an already revoked
root set. The existing event-removal gate wins over successful receipts and
terminalizes new/pending candidates without public mutations.

A new-history activity that observes a removed event does not recopy its staged
candidate. It can retry orphan/staging cleanup even when a prior attempt deleted
the source bytes. Historical media expiry is not permission to republish clips:
retired-media placement fails closed, and historical repair remains a separately
approved operation. SQL locking does not make S3 transactional or guard against
arbitrary out-of-band deletion; these guarantees cover the application-owned
revoke-before-reclaim protocol.

## Compatibility and rollout

`ff-081-reversible-selection` enables the new placement and consistent recovery
payloads only with atomic placement, exact-variant retention, accepted-validation
capture, canonical aliases and the FF-092 losing-candidate correction. Recorded
older histories omit the optional fields and retain their original commands.
No new activity registration, service, query, model call or public API is added.

Before rollout, apply the pending migration chain and deploy through separately
approved operations. Validate a natural restoration, exact recurrence, conserved
source totals, receipt retry and frontend dirty update. Watch placement latency,
HEAD counts and Temporal payload/history size: the new path reads retained
evidence and returns current keeper hashes; the earlier in-memory benchmark is
not a measurement of these integration costs. Historical corpus replays remain
topology tests, not an automatic historical repair plan.

Validation passed the full Go/PostgreSQL/scenario suite, repeated race-enabled
integration tests, saved-data comparisons and both saved incident SDK replays.
Build, vet and scoped lint pass. See the
[audit checkpoint](../design/audits/video-restoration-2026-09-12.md#workflow-integration-checkpoint)
for evidence scope and remaining production validation.
