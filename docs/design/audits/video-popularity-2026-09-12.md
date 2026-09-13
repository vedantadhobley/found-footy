# Exact and assigned source support — September 12, 2026

## Outcome and boundary

Keeper quality and source support are separate quantities. The user requested
correct scoring for reversible selection, not compatibility with old totals.
The local [selection planner](../../../internal/domain/video/selection.go)
already excludes popularity from keeper quality. It preserves each observation's
exact asset, assigns it to one selected representative, and derives selected
popularity from those assignments. These are not disjoint dHash clusters:
direct overlap is non-transitive and an observed variant can match two roots.

This pass added an offline
[support audit](../../../scripts/audit_video_quality/README.md#popularity-ownership-experiment)
and tests. No production queries, mutations, deployment, keeper-policy changes,
threshold changes or historical repairs were performed. The screen-recording
review remains explicitly deferred. FF-081 owns the scoring follow-up.

## Evidence

The initial pass used the September 8 saved CSV: 2,409 assets in 748 events.
154 events reconciled at each root; five older events had a hidden variant
matching multiple active roots, but no usable exact-source counts for those
variants. We then found the newer September 11 retained-asset export already
saved in the repo and used that as the main corpus.

Main inputs:

- Saved [FF-092 assets](../../../scratch-audit-2026-09-11/ff092-history/assets.ndjson),
  SHA-256 `d46ca1c49db95aa7a180eadf7aeb854c9f84fd48aa7d07fc94c3efe2d9371c7f`.
- [Offline converter](../../../scripts/audit_video_quality/history_to_csv.jq).
  It preserves own verification, including false, and otherwise inherits the
  recorded root category for topology only. It carries event removal and object
  reclamation markers. It detects cycles and cross-event/fixture lineage.
- Generated [CSV](../../../scratch-audit-2026-09-12/graph-popularity/retained-september11.csv),
  SHA-256 `55332f0088afbd9956bc88d94c3aa5ae136ab65a86d189339cb84ed7dabe0eb7`.
- Generated [report](../../../scratch-audit-2026-09-12/graph-popularity/report-september11.ndjson).
  SHA-256 `221a4a53b8aadc7657d31d7ed550998d2e39c6fe587c85a54d85aefcb6106011`.
  These scratch artifacts are ignored local evidence, not shipped application data.

| Main-corpus measurement | Result |
|---|---:|
| Retained assets / events | 3,015 / 876 |
| Assets without exact-source attribution | 1,819 |
| Events with reconciled per-root counts and public, unreclaimed roots | 278 |
| Assets / recorded source observations in that cohort | 1,169 / 5,062 |
| Reconciled events with multiple selected roots | 141 |
| Reconciled events with ambiguous ownership against recorded roots | 0 |
| Conditional restoration projections adding a root | 2 |

Three older events still have ambiguous active-root topology in the newer
snapshot: Isak 60′, Saka 59′ and Marquinhos 90+5′. Their exact-source counts are
missing; no historical score effect is claimed. Different snapshots' counts
must not be added together or treated as present production state.

The root totals and lineage reconcile in the qualified cohort. That is stronger
than checking only an event-wide sum, but not proof of individual source IDs:
the export counts `observed_asset_id` rows and does not include per-source
outcomes or `credited_asset_id`. Retired aggregate popularity is never used as
an exact count. Missing attribution is not evidence that a clip had zero sources.

## Why exact popularity is not selected popularity

These main-corpus examples have reconciled counts:

| Selected encoding | Exact MD5 sightings | Assigned source support |
|---|---:|---:|
| Barnes 37′, Newcastle–Bournemouth | 4 | 83 |
| Yamal 6′, Valencia–Barcelona | 1 | 68 |
| Parrott 81′, Betis–Real Madrid | 3 | 64 |

Replacing the public score with exact sightings would discard the support from
other matched encodings. It would also make representative upgrades appear to
lose popularity merely because the better encoding circulated less often.
Conversely, the assigned number is not a picture-quality measurement and must
not determine which encoding wins. A popular family can have a rare best copy.

## What restoration changes

The actual pure planner is run against each reconciled event using recorded
counts, synthetic source identities and credits reconstructed from lineage.
Existing own shares provide acceptance category. Their media is **assumed**
available unless recorded reclaimed; nothing is fetched. Never-public variants
remain ineligible because their own validation is absent. Original event/asset
UUIDs and first-seen ordering are preserved. Clock display is not replayed.

### Maitland-Niles 90+6′ — scoring can affect visibility

Event `01dedc54-da16-4e3c-9539-ac43c81ffd77`, fixture `1557390`:

- The snapshot has a main keeper at 32 and two unrelated singletons.
- The projection restores `32b50be4-0ad5-50a5-8ebe-f590cc4b57a4`, which has one
  exact sighting. The main keeper becomes 31. The restored singleton remains
  hidden by FF-078, as the user requires. All 34 observations remain counted once.
- Two never-public variants, with four and one sightings, match both the main
  and restored keeper under the existing dHash routes. Their own acceptance is
  unknown; the planner preserves their current owner.
- **If their own acceptance confirmed that category**, moving the four-source
  variant would produce 27/5; moving only the one-source variant would produce
  30/2. Either makes the restored clip visible without changing keeper quality,
  matching, selected membership or the visibility threshold.

These are conditional alternative assignments, not corrections to apply.
The graph supports both destinations; it does not establish the objectively
right owner or prove full-video substitutability.

### Mastantuono 30′ — score changes need not change display

Event `d7db1660-1055-4651-b3ef-75d4e6dc3ad3`, fixture `1550126`:

The saved pre-repair snapshot projects from 36 on one verified keeper to 27/9
across two. One unrelated unverified singleton remains hidden. Three other
never-public variants match both verified keepers and have two, two and one
sightings. Conditional single-variant reassignment changes the split to 25/11
or 26/10, without reversing their order or changing visibility. This is saved
incident evidence, not a fresh check of the separately repaired production event.

No known-own-evidence ambiguous reassignment was available in the reconciled
recorded or projected cohorts. The conditional cases demonstrate a design
boundary; they do not establish an observed production ranking incident rate.

## Scoring design implication

Keep four responsibilities explicit:

1. **Exact support:** count attributed accepted sources for each observed MD5.
   Observation identity is fixed; the count grows as new sources arrive.
2. **Representation:** choose suitable selected clips using content/quality
   evidence. Popularity is not a substitute for this evidence.
3. **Ownership:** assign each source to one selected representative. Exact
   selected variants own their sources; hidden variants need a valid assignment.
4. **Public projection:** derive assigned popularity, then apply the accepted
   verification/ranking/visibility policy. Never carry a retired root's stale
   aggregate forward as independent evidence.

The implemented rule preserves a still-valid owner; when reassignment is needed,
it uses the existing quality comparison in stable order. This conserves sources
and avoids popularity-driven feedback, but it is an explicit attribution policy,
not mathematical proof that a selected clip uniquely deserves those votes.

The remaining product question is how competing suitable representatives should
own ambiguous support. Compare faithful representation using overlap/content
evidence before changing that rule. Do not select an owner merely to push a clip
over the singleton threshold, use a transitive component total, duplicate votes
across roots, or feed assigned popularity back into keeper selection. A proposed
non-exclusive/fractional metric would require an explicit contract change.

This audit does not adopt a new quality score, overlap threshold or owner rule.
It makes the distinction measurable so that the next decision is about scoring
meaning, not preserving old output by accident.

## Verification and limitations

The audit-package suite, including the saved September 11 case, passed with
the race detector; Go vet and scoped pinned golangci-lint passed. Converter
controls preserve explicit false verification and reject inherited category
through cycles or cross-scope lineage. Synthetic regressions cover the 2/2/20 bridge,
source conservation, restoration splits, missing validation, removed/reclaimed
state, per-root reconciliation, cycles, scopes, visibility asymmetry, unknown
rank ties, deterministic output and conflicting modes. No schema/runtime code
changed in this pass; the database suite was not rerun.

Counterfactuals move one complete MD5's observations at a time. They do not
enumerate every combined assignment. Individual minimum/maximum counts vary
only known-own-share alternatives and are not jointly attainable extrema.
Rank tests count strict reversals only among clips visible before and after;
the export lacks share creation times needed to resolve full ties. dHash
remains 12/30/3 or 16/50/5, and no new AI/media analysis or human label was added.
