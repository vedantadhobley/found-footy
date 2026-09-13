# Direct-match restoration experiment — 2026-09-12

## Result and scope

The experiment retains both nonmatching endpoint clips in all six arrival
orders of the saved Mastantuono triangle. FF-092 alone retains both in four
orders. The larger saved corpus changes five first-observation-order final sets.

This is offline evidence, not a production repair or an accepted keeper policy.
No application, schema, production data, model or media changed. The
[reversible-selection proposal](../proposals/reversible-video-selection.md)
records the implementation boundaries and remaining policy decisions.

**Subsequent September 12 decision:** the user retained FF-078 unchanged, with
no exemption for restored or independent clips. The visibility question raised
by this audit is settled in the proposal; the experiment below still measures
selected roots before that filter, not public output.

## Method

The audit command's `-restoration-json` mode replays ordinary FF-092 placement
and compares it with an additive pass after each arrival. That pass restores
an observed asset when no selected keeper directly matches it. It does not use
future arrivals, a hidden intermediary or a popularity tiebreak.

The production comparator and both dHash routes are unchanged. Pools remain
separate by event, verification category and hash version. Historical lineage
can connect an analysis component, but only measured direct matches support
hiding a node. Isolated nodes need no restoration comparison and are omitted.

The original audit reducer still represented pre-FF-092 behavior. It is now
explicitly named `simulatePreFF092Policy`, and the ordinary text report labels
that historical baseline. Its old experiments and human labels are preserved.
The new mode supplies the explicit FF-092 comparison.

Direct matching is necessary evidence, not whole-video replacement suitability.
This experiment tests topology; it does not determine which footage shows the
correct goal or whether unmatched content is useful.

## Exact incident regression

The checked-in
[Mastantuono hashes](../../../internal/workflow/testdata/mastantuono-bridge.csv)
retain the incident's three MD5 variants. Here A beats C, C beats B, A/C and
B/C match, and A/B does not. These endpoint quality labels are the reverse of
the abstract A → C → B example; the invariant is the same.

| Arrival order | FF-092 final roots | With direct restoration |
|---|---|---|
| A, B, C | A, B | A, B |
| B, A, C | A, B | A, B |
| A, C, B | A, B | A, B |
| B, C, A | A | A, B |
| C, A, B | A, B | A, B |
| C, B, A | A | A, B |

In C, B, A, B never becomes a keeper before restoration. Testing only formerly
public shares would miss that case. In this incident A is wrong-event footage;
keeping A alongside B is not a semantic-validation fix. FF-003 remains separate.

## Saved-corpus results

Input: the existing September 8 export, 2,409 accepted assets. It contains 479
multi-node match/lineage components covering 1,490 assets in 447 events. The
remaining 919 assets are isolated. Components are scoped analysis groups,
not transitive duplicate identities.

Each component visits at most 1,000 deterministic arrival orders. Of 479
components, 451 are exhaustive and 28 sampled.

| Measurement | Result |
|---|---:|
| Visited arrival orders | 38,464 |
| Orders with changed final roots | 1,955 |
| Components with any changed visited-order result | 11 |
| Changed first-observation-order final sets | 5 |
| Restoration operations in those chronological runs | 6 |
| Final unsupported nodes under the chronological FF-092 replay | 5 |
| Components arrival-sensitive under FF-092 | 30 |
| Components still arrival-sensitive after restoration | 21 |

Every experimental prefix has selected direct support. Restoration does not
remove the comparator's general arrival dependence or its known Danso cycle.

The five chronological changes are review leads:

| Event | Fixture | FF-092 roots → restored roots |
|---|---|---|
| Messi 83′, Inter Miami–Montreal | 1490422 | 1 → 2 |
| Fermín 71′, Elche–Barcelona | 1570346 | 1 → 2 |
| Lee Kang-In 70′, Atlético–Málaga | 1570334 | 1 → 2 |
| Welbeck 50′, Chelsea–Luton | 1623102 | 1 → 2 |
| Marquinhos 90+5′, Lille–PSG | 1552746 | 2 → 3 |

These are not counts of production losses or verified improvements. Asset
first-observation order is not the asynchronous placement-completion order.
Original validation and media availability are not rechecked. The report marks
nodes lacking their own share; the six chronological restorations all have
one, but that does not prove that their original bytes remain available.

## Implementation-surface audit

1. **Own validation:** `video_shares` retains a public clip's matched minute;
   never-public `video_assets` do not retain their own equivalent. The export's
   inherited verification bucket must not become promotion evidence. FF-093 is
   the shared durable-record boundary.
2. **Recovery:** `LoadEventAssets` supplies live roots plus exact aliases, not
   every eligible historical variant with its own evidence.
3. **Transaction:** placement accepts one winner and a loser list;
   `ensurePlacementShare` rejects a superseded share. General restoration needs
   an atomic selection change, not an isolated share-state update.
4. **Credits and aliases:** `observed_asset_id` permits exact source ownership
   to split while `credited_asset_id` changes. Workflow aliases currently only
   redirect whole roots. Both durable and in-memory ownership must change
   consistently before another exact recurrence.
5. **Visibility and URLs:** FF-078 may still hide a restored singleton. Existing
   share IDs and own clock metadata must survive, and redirected URLs must
   follow committed selection. Selected roots are not automatically public output.
6. **Retention and history:** unavailable/reclaimed assets are not candidates
   for automatic resurrection. Current alias mapping and historical replacement
   decisions must remain distinguishable; a rewrite must not erase audit evidence.

The next implementation foundation is bounded own acceptance evidence, then a
pure planner and event-locked selection transaction. No new graph service or
frontend protocol is needed. Quality and visibility policy are explicit remaining
decisions, not prerequisites for collecting more model benchmarks.

## Reproduction and verification

See the [restoration command](../../../scripts/audit_video_quality/README.md#restoration-experiment).
Two runs against the saved CSV produce byte-identical NDJSON. Source and output
are local ignored evidence, not committed media:

- Input: `scratch-audit-2026-09-08/retained-quality-corpus.csv`.
  SHA-256: `0b4b20df9fbe4636ab80663dfb4f0a717756a82dbf75af4d31e0ba48181623b0`.
- Output: `scratch-audit-2026-09-12/graph-restoration/report.ndjson`.
  SHA-256: `888d362c4b0c82fd634f317c63667a82b9c4591398088ba35ca238dcf0f92ef9`.

A warmed-cache rerun took 1.337 seconds in an offline four-CPU/4-GiB container,
including `go run` startup. This is not a production latency estimate: the
experiment precomputes pair matches and replays cached matrices, with no
PostgreSQL, S3, decoding or model calls.

Passed: audit-package tests; three repeated race-enabled runs of the audit and
domain-video packages; `go vet`; and the pinned linter with zero issues. Tests
cover all six exact incident orders, stronger bridges, never-public restoration,
generated graph prefixes, direct/directional support, exact recurrence, unchanged
quality-cycle sensitivity, scope validation, deterministic reports and failure
propagation. Full application/PostgreSQL gates were not run for this offline-only
slice. Nothing was deployed or repaired.

## Domain and transaction implementation checkpoint

Subsequent September 12 work implements the domain planner and internal atomic
reselection repository; see the
[decision](../../decisions/2026-09-12-restored-clips-keep-one-source-owner.md).
This supersedes the original experiment's code-only scope, not its historical
results. EventWorkflow still does not invoke reselection. No production query,
mutation, fresh model call or media download was made for this slice.

The exact Mastantuono hashes pass all six arrival orders through the new
domain planner. The opt-in saved-CSV check visits 1,490 first-observation-order
prefixes across the same 479 multi-node components and agrees with the earlier
experiment at every prefix. It uses synthetic own-share acceptance and one
synthetic source per MD5, so it tests topology and vote conservation, not actual
historical popularity, validation or repair eligibility. Isolated assets remain
outside this component comparison. No human quality label changed.

The warmed offline four-CPU/4-GiB run took 1.695 seconds inside the test,
including comparison graph construction and replay. Individual planner calls
measured median 94 µs, p95 1.732 ms and maximum 4.945 ms. These are sampled
in-memory costs, not production latency: no SQL row-lock time, S3 existence
checks, concurrent placement or decoding/model work is included. Matches are
cached inside each planner call, not yet across placements.

The replay preserves ordinary placement's incumbent comparison order and
appends restored roots. A plan's set ordering is not a new quality policy.

Real PostgreSQL regressions separately exercise existing-share and never-public
restoration, the A=2/C=5/B=7 split to A=2/B=12, alias recovery, unchanged
singleton visibility, one-to-two recurrence, immutable receipt retries, changed
request rejection, concurrent stale snapshots and late failure rollback. Domain
regressions also pin one-owner credit when several selected clips match: preserve
a valid owner, otherwise use existing quality and incumbent-tie behavior.
The repository
requires actual complete source attribution and caller-prepared media; it does
not accept the synthetic corpus adapter as production evidence.

Verification passed the full Go suite, including disposable-PostgreSQL migration
and transaction tests, three repeated race-enabled selection/PG checks, repeated
domain/audit race tests, application/tool builds, vet and scoped application/
script/test lint. The existing ignored scratch-helper formatting failure remains
outside scoped lint; the aggregate workspace lint gate is not claimed clean.
The new migration's schema fingerprint matches the fresh-install snapshot.

At this checkpoint, the next slice was to integrate incoming placement and reselection into one
transaction, perform real media preparation/checks, preserve incumbent comparison
order, version workflow/cache recovery and test the existing notification path.
The new repository method is not a separately scheduled repair workflow or an
authorization to apply the saved experiment to production.

## Workflow integration checkpoint

Subsequent local work integrates the planner into the incoming placement
transaction, with real-object preparation, current-state retry/recovery and
versioned notifications. The
[integration decision](../../decisions/2026-09-12-selection-commits-with-incoming-placement.md)
records its contracts, incomplete-history skip, retention locking and rollout
requirements. The saved topology/quality results above are unchanged; no live
fixture query, media fetch, historical repair or production operation was made.

New real-PG tests cover combined rollback, missing new proof/media, skipped
historical credit, old receipt replay after later placement, and concurrent
revocation. A real-PG/activity test with a failure-injecting object double proves
never-public restoration, lost cleanup acknowledgement, fresh retry state and
removed-event retry with absent staging. Workflow tests cover cache/alias
replacement before publication, exact vote conservation and both marker branches.
The existing two saved incident SDK histories replay with the new marker.

Final verification passed the full Go suite (including real PostgreSQL and
scenario tests), three repeated race-enabled integration/workflow runs, build,
vet and scoped lint. The saved 2,409-asset comparison still passes all 1,490
prefixes across 479 components with the same synthetic-evidence limitation.
Edited-document links and `git diff --check` pass. The earlier unrelated ignored
scratch-helper lint limitation remains; no aggregate workspace lint claim is made.
Changes remain uncommitted and undeployed at this checkpoint.

## Review correction: event-local validation lookup

The placement snapshot queried accepted evaluations by event, while the original
index began with asset ID. An isolated review probe returned three evaluations
only after scanning 20,000 unrelated rows (5,001 shared blocks). The corrected
event-leading `(event_id, asset_id, recorded_at, id)` index supports the same
earliest-evaluation ordering without changing selection policy or evidence.

The [index-only migration](../../../migrations/20260912_03_index_event_validation_lookup.sql)
extends the ordered chain without editing its previous migrations. Fresh schema
and the startup manifest include the index. The existing asset-leading index
still serves asset-local history and foreign-key checks; no JSON payload is
duplicated into either index.

The regression test passes for fresh adoption and upgrade/retry: the indexed
lookup returns three rows, touches four shared blocks and filters no unrelated
rows. Snapshot fingerprints and the evaluation count remain unchanged, and
startup rejects a missing required index. This measures database work, not
production request latency.

The saved-data rerun still passes all six Mastantuono arrival orders and the
1,490 prefixes across 479 multi-node components of the 2,409-asset export.
Acceptance/source votes remain synthetic in that topology regression. Both saved
incident SDK histories replay; scoped database-package lint reports no issues.
The stale FF-093 next-step wording now routes to the implemented integration.

The subsequent capped `make test` run did not finish the PostgreSQL package:
Go's ten-minute package timeout fired during
`TestPlacementSelectionGuardsAndRollback/missing_new_variant_bytes`. That
subtest had run for one second and was waiting for its disposable PostgreSQL
container to start; its parent had run for sixteen seconds. The captured run
reported no assertion failures before the timeout. All other packages passed
from cache. That attempt did not establish a full-suite pass for the index
correction; it required a complete database-package rerun with retained output.

The September 12 rerun completed at 23:30 UTC with exit status zero:
`go test -buildvcs=false -count=1 -json -timeout=30m ./internal/infra/pg`.
All 110 top-level tests and 43 subtests passed, with no failures or skips, in
305.704 seconds. This includes the previously interrupted placement guards,
restoration, source accounting, removal, and fresh/upgrade index migrations.
No source correction was needed, and the database/migration input fingerprint
was unchanged across the run. The pinned Go container used four CPUs, a 6-GiB
cap and a 4-GiB Go memory limit; disposable database caps remained unchanged.
The runner and disposable databases exited after completion.

Full JSON output is retained locally at
`/tmp/found-footy-db-suite.03jJRC/tests.jsonl`, with the successful process marker
in `result.txt` beside it. The log SHA-256 is
`f6440053d30b203d2b37d702ee654449bced202447d67a99c5b37c5904ce6ad9`.
This closes the database-verification blocker. It does not change Makefile's
default timeout or claim the aggregate workspace formatting/lint gate is clean.

All work remains local; this is not a production migration or a historical clip
repair.
