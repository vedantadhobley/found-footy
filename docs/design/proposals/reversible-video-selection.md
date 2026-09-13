# Reversible video selection — FF-081

## Status and scope

The September 12 discussion agreed that an older clip must be reconsidered
when its substitute stops being selected. In A → C → B, a historical path
through C does not prove that selected B replaces A.

The first delivered slice was an offline experiment and
[implementation-surface audit](../audits/video-restoration-2026-09-12.md).
The second slice implements FF-093's own accepted-validation record in local
application code and an additive migration; see the
[decision](../../decisions/2026-09-12-accepted-validation-follows-exact-assets.md).
The third slice implements the pure domain planner and atomic repository
reselection for already accepted data; see the
[selection decision](../../decisions/2026-09-12-restored-clips-keep-one-source-owner.md).
The fourth slice now integrates it into new-history placement, media checks,
cache recovery and notifications; see the
[integration decision](../../decisions/2026-09-12-selection-commits-with-incoming-placement.md).
Nothing is deployed, and the accepted quality/visibility policies did not change.
The September 13 [direct-support decision](../../decisions/2026-09-13-popularity-counts-direct-support.md)
now supersedes exclusive popularity allocation. Canonical aliases still have one
destination; public scores count direct matching evidence per clip.
[FF-081](../../todo.md#ff-081--pairwise-quality-policy-is-not-a-stable-cluster-order)
owns remaining work. Existing quality judgments, dHash thresholds, clock
validation and popularity visibility remain unchanged.

## Required invariant

Every eligible variant hidden by deduplication needs a **currently selected,
direct, suitable substitute**. A path through a hidden variant is insufficient.

| Evidence | Selected set | Consequence |
|---|---|---|
| C adequately replaces A and B | C | Both can remain hidden. |
| B replaces C, but not A | B | Reconsider A; C cannot justify hiding it. |
| A and B have no suitable selected substitutes | A, B | Retain both. |

Failure to match is missing replacement evidence, not proof that two videos
show different actions. Conversely, a dHash match proves shared frames, not
whole-video substitutability or correct event attribution.

Keep three questions separate:

1. What footage matches? Use measured pairwise overlap.
2. Is one clip an adequate replacement? Apply the accepted content/quality rule.
3. Which clips remain selected? Require direct selected support for hidden clips.

No new coverage percentage, fitted quality score or popularity tiebreak is
accepted here. The offline experiment supplies direct dHash matches as a
necessary support check. After ordinary FF-092 placement it restores unsupported
observations in first-observation order. Restoration only adds keepers; it
does not retire another keeper. This is not a minimum-cover algorithm or an
arrival-independent quality selector.

## Existing foundation and gaps

FF-083 retains accepted event/MD5 assets, including variants that never became
public, and distinguishes observed source identity from credited keeper identity.
That is the foundation; it is not automatic reselection.

The surface audit found these production gaps:

- A previously public asset retains its own timestamp metadata on its share.
  A never-public accepted variant does not retain its own matched timestamp.
  The corpus export inherits its keeper's verification category for analysis;
  that is not evidence for a future promotion. FF-093 now closes the new-record
  gap locally, but does not reconstruct older missing observations.
- Workflow recovery loads current roots and exact-MD5 aliases, not all hidden
  eligible variants with their own validation evidence.
- The placement transaction merges onto one winner. It cannot atomically
  restore a share, split credited ownership, and replace the complete selected set.
- Runtime exact-MD5 aliases redirect whole retired roots. Restoration requires
  splitting those aliases by observed asset, not merely adding a keeper.
- `superseded_by` records direct placement decisions and supplies live alias
  routing. Reversible routing must not erase historical decision evidence.

The durable accepted-validation record belongs with
[FF-093](../../todo.md#ff-093--accepted-vision-evidence-expires-with-workflow-history).
Do not add a second competing validation store solely for selection. Unknown
historical observations must remain unknown; never copy the old winner's clock
onto a newly selected variant.

## Proposed implementation boundary

Use a pure Go selection planner and the existing event-locked PostgreSQL
placement adapter. No graph database, service, workflow family or frontend
protocol is needed.

The event's serialized placement lane should:

1. Retain the incoming variant and its own bounded acceptance evidence.
2. Load eligible retained variants and current selection/ownership. Cache
   pairwise match evidence within that scope; do not rerun media extraction
   merely because selection changed.
3. Produce a complete proposed keeper set, direct replacement witnesses and
   source-credit assignments from that snapshot.
4. Commit the selection atomically under the event lock. Reject stale snapshots,
   removed events, invalid targets or unavailable media. Update shares, current
   aliases, credited ownership, popularity and ranks together; retain an audit
   record of the decision.
5. Replace the workflow's keeper snapshot and exact-MD5 aliases from the committed
   result before its next placement. Publish the existing `event.update` only
   after commit, using the current durable notification path.

S3 operations cannot be part of a SQL transaction. Prepare required media
before publication and retain the existing recovery/idempotency boundary.
`object_reclaimed_at IS NULL` alone does not prove that bytes exist. Do not
resurrect reclaimed media or reset a removed event. Routine reselection should
not need a new Twitter search, vision call or hash extraction when the asset's
own durable evidence and media are sufficient.

This needs Temporal versioning: old histories retain their recorded behavior;
new histories use the new snapshot and placement contract. A deployment is not
authorization to repair historical production data.

## Direct support and canonical routing

Each accepted source contributes once to each selected clip its observed MD5
directly matches. Exact self-observations are included once, not added again.
Keep own-validation, event and hash-version boundaries; do not follow a graph
path for support. A source can support multiple keepers, so scores are not
additive across an event. Retained acceptance/hash evidence can contribute after
source-byte reclamation, even though the source clip cannot be restored.

For A=2, bridge C=5 and B=7, selecting A and B gives A=7 and B=12 when C matches
both and A/B do not match each other. A subsequent C sighting increments both
scores once. Recompute from accepted candidate `observed_asset_id` records; do
not transfer or add cached scores when a new clip replaces several keepers.

Canonical alias/credit routing remains single-owner. Preserve a current valid
destination; reassignment uses the existing quality comparator and stable order.
That choice no longer allocates popularity. Unknown own acceptance cannot supply
direct evidence; incomplete source attribution keeps the explicit legacy fallback.
No new score table, graph service or quality threshold is introduced.

Reuse an asset's existing share ID and own validation when restoring it.
Preserve creation/history semantics and satisfy compatibility-rank uniqueness.
Resolve shared URLs through the new committed mapping. Never temporarily expose
a restored share while its exact aliases still credit the old winner.

## Public visibility stays unchanged

The [September 12 source-support audit](../audits/video-popularity-2026-09-12.md)
separates exact-MD5 observations, representative quality, source ownership and
public ranking. Conditional historical examples show that ambiguous ownership
can change the singleton filter's result even with a fixed selected set. Its
exclusive ownership baseline is now historical; direct scoring was subsequently adopted.
This is a scoring-semantics follow-up, not a request to preserve old totals or
exempt restored clips from visibility rules.

The subsequent [direct-support comparison](../audits/video-direct-support-2026-09-13.md)
tests non-exclusive per-clip evidence as a public score, independent
of exclusive ownership. It changes conditional scores in two historical
restoration cases, not keeper selection. The user adopted direct scoring on
September 13; the linked decision records its non-additive semantics and evidence
limits. The experiment's conditional legacy validation is not adopted as proof.

**Confirmed by the user on September 12:** restoration does not override
[FF-078's singleton filter](../../decisions/2026-08-30-popularity-prunes-public-singletons.md).
Independent content is not an exemption either. Selection determines eligible
keepers; the existing API policy determines which keepers appear by default.

- A timestamp-verified keeper at popularity three or more suppresses every
  popularity-one keeper for that event.
- An unverified keeper at three or more suppresses only unverified singletons.
- Popularity two or more is not suppressed by this filter. A restored singleton
  can appear when another accepted discovery raises its direct support to
  two, or when the qualifying threshold keeper leaves the selected set.
- A restored singleton retains its share and media subject to normal retention;
  hiding it from the default listing is not deletion or supersession.

The rationale is to require repeated support for an alternative once a qualifying
keeper has stronger source support. Popularity measures directly matching discoveries,
not guaranteed picture quality. The same rule applies regardless of whether a
keeper is newly selected or restored; no arrival-history exception is added.

Integration tests must cover restored popularity one versus two, absence of a
threshold keeper, and verified/unverified asymmetry. Recompute visibility from
the committed direct-support values, not retired aggregates or exclusive routing.

## Delivery sequence

1. **Done: offline proof and surface audit.** Replay FF-092 and additive direct
   restoration; preserve the exact incident, all arrival orders and earlier
   human judgments. Record corpus limits rather than treating output as a repair.
2. **Implemented locally: own accepted-validation evidence.** FF-093 adds the
   bounded record, schema migration, retention and unknown historical/replay behavior.
   The complete test suite and saved incident replays pass; no deployment yet.
   Previously public clips can support a bounded restoration without this, but
   arbitrary never-public variants cannot safely inherit another share's clock.
3. **Implemented locally: planner and reselection transaction.** Own-evidence
   eligibility, direct support, one-owner routing, repeatable snapshots,
   optimistic freshness and immutable receipts are implemented for already
   accepted data. Real PostgreSQL tests cover shares, credit, retries, concurrent
   stale plans, rollback, removal, aliases and visibility. Saved hashes reproduce
   the offline topology experiment with synthetic acceptance/votes.
4. **Implemented locally: workflow integration.** Incoming placement and
   reselection share one transaction. Real object checks, versioned consistent
   recovery, current-state retry results, alias replacement and notification
   ordering are implemented. Incomplete legacy credit skips only graph repair,
   with an explicit receipt/warning; it does not block ordinary accepted placement.
5. **Next: approved rollout and natural validation.** Apply the pending migrations
   and deploy only after separate approvals. Direct scoring is implemented locally
   within the same transaction, with a receipt-constraint migration and no new fields.
   Validate source identity, non-additive per-clip counts,
   natural restoration, exact recurrence, frontend updates and measured overhead.
   Historical repair remains a separate operation.

Required regressions include all A/B/C arrival orders; a bridge that legitimately
replaces both clips; restoration of a never-public variant; no future-node
leakage; exact recurrence and retry-safe per-clip support; quality cycles;
event/verification/hash-version separation; unknown validation and missing media;
removed events; restored share identity/ranks; and unchanged FF-078 visibility.

Do not block the topology correction on a perfect learned quality model. Also
do not present the match-only experiment as resolving FF-081's content and
quality policy.
