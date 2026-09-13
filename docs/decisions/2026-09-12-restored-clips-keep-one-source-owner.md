# Restored clips keep one source owner

## Status

**Partially superseded on September 13:**
[popularity now counts direct source support](./2026-09-13-popularity-counts-direct-support.md).
The one-owner rule below remains for canonical alias/credit routing, not public
score allocation or aggregate-count validation. The original implementation
record below preserves the prior rationale; neither version has been deployed.

The pure planner and atomic repository operation are implemented locally for
FF-081. Subsequent work now integrates them into new-history placement; see the
[integration decision](./2026-09-12-selection-commits-with-incoming-placement.md).
Nothing is deployed. The standalone repository contract below remains useful
for tests; routine workflow placement uses one combined transaction.

## Selection rule

After ordinary FF-092 placement, reconsider accepted variants in stable
`first_seen_at`, asset-ID order. Restore a variant when no selected clip directly
matches it within the same verification/hash-version pool. Keep existing roots;
restoration never retires another root. dHash remains 12/30/3 or 16/50/5 under
the supplied recorded matcher policy. A policy with no sustained route retains
the primary-only behavior.

The planner caches direct comparisons inside its immutable snapshot. It does
not infer replacement from transitive connectivity, introduce a cluster ID,
change `IsUpgrade`, or add a coverage/quality threshold. Direct matching is
necessary evidence, not proof that a clip adequately replaces every part of
another clip or depicts the right event. General quality cycles and arrival
dependence remain open.

Current roots must have active shares and prepared, unreclaimed media. A hidden
variant is eligible only when its own acceptance is known, its media was prepared
by the caller, and neither its share nor object is removed/reclaimed. An existing
share preserves its original verification/minute. A never-public variant uses
the earliest retained FF-093 evaluation, ordered by recorded timestamp then ID;
the planner never picks the most favorable later verdict. Missing historical
acceptance stays unknown.

## Source ownership

Each accepted source still contributes exactly once:

1. A selected asset owns observations of its exact MD5.
2. Otherwise preserve its current selected owner when the direct match remains
   valid. Ineligible historical variants keep their previous canonical owner;
   this is preserved lineage, not newly inferred match evidence.
3. If reassignment is required, compare directly matching selected candidates
   with the existing `IsUpgrade` relation. Existing roots are visited in stable
   first-seen/ID order, followed by restorations in that order; the incumbent wins
   a tie. Popularity never feeds back into this choice.

The planner requires complete accepted-source identities, immutable observed
attribution, current root credit, and matching aggregate root counts. Missing or
inconsistent historical attribution returns `ErrSelectionCredits`. It does not
split a stale retired popularity value or invent an unattributed remainder.
For A=2, C=5 and B=7 with A → C → B, restoring A yields A=2 and B=12, not sixteen
votes. Unknown histories need explicit review, not automatic repair.

The plan's selected set is not a public rank or a new quality-tournament order.
Workflow integration must preserve the existing incumbent comparison order and
append restorations deliberately; it must not silently sort by plan output.

## Atomic application

`PlacementRepo.LoadSelection` reads a repeatable snapshot of assets, own shares,
canonical accepted evidence and accepted source attribution. Its fingerprint
covers included planning inputs. `CommitSelection` takes the common event lock,
locks relevant asset/share/source rows, and rejects a changed snapshot or removed
event before writing. The pure planner runs against this locked snapshot.

One transaction applies exact ownership pointers, current candidate credit,
conserved root popularity and share restoration. Existing shares retain their
ID, creation time and own clock; a never-public eligible root receives a share
only now. Compatibility ranks receive collision-free slots; public rank remains
read-derived. This operation only adds selected roots and reroutes hidden ones.

Candidate outcome, detail and outcome time continue to describe the original
placement. Reselection changes `credited_asset_id`, not that historical verdict.
The new `video_selection_commits` receipt preserves the old topology, aggregate
and exact-source counts, share/evaluation identity, policy and resulting plan.
It does not duplicate dense hashes or model payloads. JSON receipt sections are
bounded to one MiB each and remain with SQL event history after media reclamation.

A request UUID is bound to its snapshot, scope, policy and prepared-asset set.
Identical retries return the original decision without rewriting later state;
changed content under the same UUID fails. A replayed receipt is historical, not
a fresh cache snapshot: callers must reload before treating it as current state.
The event-removal gate still wins over a previously successful receipt.

The additive
[`20260912_02_add_reversible_selection.sql`](../../migrations/20260912_02_add_reversible_selection.sql)
creates only the receipt store. Migration changes no historical clip, source,
share or popularity. Both migrations and any rollout require separate approval.

## Integration constraints and disposition

This standalone repository operation applies already accepted data. Incoming
placement and reselection now share one transaction; no second repair activity
exposes an intermediate selection. The integration decision records the current
workflow path and the explicit incomplete-history skip boundary.

Prepared IDs are an internal caller attestation, not an S3 existence check.
The integrated activity checks/copies required objects and coordinates with
revocation. SQL's unreclaimed flag alone is insufficient. The versioned workflow
replaces current roots/aliases from committed state and publishes the existing
`event.update` after the complete durable operation. No new NATS subject or
frontend contract is needed.

FF-078 visibility stays unchanged, including its verified/unverified asymmetry:
a restored singleton may remain hidden until a second credited source arrives.
Historical repairs remain separate, explicitly approved operations.
