# Popularity counts direct source support

## Status

Approved by the user on September 13 and implemented locally for FF-081.
Not deployed. This supersedes the exclusive-popularity rule in
[restored clips keep one source owner](./2026-09-12-restored-clips-keep-one-source-owner.md),
not its topology, alias routing, quality comparator or media eligibility rules.
The [saved-data comparison](../design/audits/video-direct-support-2026-09-13.md)
motivated the decision; its conditional historical scores are not repair proof.

## Score contract

For each selected clip, count each accepted source observation once when its
observed MD5 is the clip itself or directly dHash-matches it. Keep the existing
event, own-verification category and hash-version boundaries. The two matcher
routes remain 12 Hamming / 27 of 30 and 16 Hamming / 45 of 50; matching both
routes still counts a source only once. Do not traverse an intermediate variant.

One source can support several selected clips. Their popularity values therefore
overlap and **must not be summed to count unique event sources**. Exact-variant
support remains the count of distinct accepted candidates with that
`observed_asset_id`. It is part of direct support, not a second bonus added to it.

Example: A has two exact sightings, bridge C five and B seven. C matches both;
A and B do not match each other. Selecting A and B produces A=7 and B=12 from
fourteen source observations. Another C sighting raises both scores once.
If a new bridge D replaces both, its score is recomputed from individual source
records, not from A's and B's overlapping aggregates.

## Evidence and routing are separate

Each observed MD5 still has one canonical destination for exact aliases and
`credited_asset_id`. The existing owner rule remains for that routing. It does
not allocate exclusive popularity. A score for a fixed clip/evidence set does
not depend on which matching keeper wins an owner or quality comparison.

Require the source variant's own retained acceptance: its non-removed share,
or the earliest FF-093 accepted evaluation when it has no share. Never inherit
a replacement's validation category. Unknown or removed acceptance contributes
no direct support. Already accepted source evidence survives byte reclamation
or missing source media; those bytes cannot be restored, but their retained
hashes and acceptance can still support another playable selected clip.

The planner checks distinct accepted-source IDs, valid observed assets and
canonical credit routing. Every retained asset must have an attributed source.
Cached popularity is no longer an attribution checksum: valid per-clip scores
need not sum to the ledger total. Recompute rather than trust a stored aggregate.

## Atomic implementation

The existing combined placement transaction retains candidates and own evidence,
applies ordinary placement, then selects roots and derives all their direct
scores before commit. Exact recurrences use the same boundary, including a
hidden MD5 that supports several roots. Intermediate legacy increments/merges
are never published as direct scores. No support-membership table or second
write operation is needed.

The planner aggregates observations per MD5 and reuses cached direct comparisons
for scoring. It does not perform a dHash comparison per tweet. Selected scores
overwrite cached values; unchanged score rows are not rewritten. Selection
receipts record `direct-restoration-support-v2`, the policy and resulting scores.
Existing receipt retries return the original decision; activity recovery reloads
current state before updating workflow caches and publishing `event.update`.

Keeper quality and FF-078 are unchanged. Verified support at least three still
hides all singletons; unverified support at least three hides only unverified
singletons. Restored clips have no exemption. The new evidence score can change
rank or visibility without changing those rules. It measures matching footage,
not picture quality, full-content equivalence or correct goal attribution.

## Rollout and legacy boundaries

The graph integration has not shipped. Its existing
`ff-081-reversible-selection` marker enables this policy for new histories;
there is no deployed assigned-graph version to maintain. Pre-marker histories
retain their old activity commands and assigned scoring. Historical rows are
not automatically rescored at deployment.

The existing incomplete-attribution fallback remains explicit: incoming placement
can commit ordinary legacy scoring with `Skipped=incomplete_credits`, but does
not invent a direct score or restore variants from an incomplete source ledger.
Unknown own acceptance and missing source identity are distinct cases; the former
excludes unsupported evidence, while the latter prevents safe reselection.

No columns or tables are added for this policy. The
[migration](../../migrations/20260913_01_enable_direct_support.sql)
allows both old and direct-support receipt versions in the existing CHECK and
updates database column descriptions. It changes no candidate, receipt, clip or
score. The earlier FF-081/093 migrations and
deployment still require separate approval. Historical rescoring or repair also
requires a separate, bounded operation.

## Verification scope

Regressions cover multiple supported keepers, no transitive credit, both matcher
routes without double counting, short exact hashes, independent alias choice,
unknown/removed acceptance, reclaimed source bytes, exact recurrence, and stale
cached aggregates. Transaction tests cover replacement of overlapping keepers,
retries, rollback, concurrent removal and unchanged singleton visibility.
Workflow tests require both scores to reach the cache before the dirty update.
Saved-history audit modes retain their exclusive-assignment baseline explicitly
so adopting this policy does not rewrite earlier experimental evidence.

Verification on September 13 passed the complete race-enabled Go/integration
suite (`go test -buildvcs=false -race -count=1 -p=2 -json -timeout=30m ./...`),
saved-corpus regressions, build, vet, scoped lint and relative link checks. The
initial database run caught the old receipt-version CHECK; the migration above
corrects it, and the complete rerun passed. Its test log SHA-256 is
`23733277317a30b127a0cdb5c2294033beba40b9ecdb9c5a3976b7bb49419698`.
The historical direct-support report still reproduces byte-for-byte as
`086cd5ede2955af7aa5b808ae65931a285a43016e278ed86fd32b802b458a301`.
Only disposable test databases and saved local exports were used.
