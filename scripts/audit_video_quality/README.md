# FF-081 retained video-quality audit

This command reconstructs the retained perceptual-match and supersession graph,
replays arrival orders, and compares diagnostic keeper policies. It consumes
CSV on standard input. It has no database client, credentials, or write path.

FF-083 exports every retained accepted MD5, including a losing variant that
never received a public share. Such a row uses `share_state=observed` and an
empty `share_id`; its timestamp-verification category is inherited from the
live root reached by committed supersession edges. `superseded_by` remains a
direct decision edge. The command may analyze graph topology but must not treat
connected components as transitive duplicate identity.

`popularity` remains the aggregate credit stored on an asset while it acts as
a root. `observed_popularity`/`*_exact_observations` is the distinct-MD5 source
count derived from candidate `observed_asset_id`; this is the evidence for
comparing how often each encoding occurred.

`query.sql` is a read-only Postgres transaction. Export its result once, then
run every policy experiment against that offline file:

```bash
docker exec -i found-footy-prod-postgres \
  psql -qAt -v ON_ERROR_STOP=1 -U ffuser -d found_footy \
  < scripts/audit_video_quality/query.sql > /tmp/ff081-corpus.csv

docker run --rm -i -v "$PWD:/src" -w /src golang:1.25.11-bookworm \
  /usr/local/go/bin/go run ./scripts/audit_video_quality \
  -max-permutations 100000 -details 30 \
  < /tmp/ff081-corpus.csv
```

Production export still requires explicit approval under the project operating
rules. The offline analysis does not.

The total-order scores are comparisons, not accepted production policy. The
focused [2026-08-31 audit](../../docs/design/audits/video-quality-2026-08-31.md)
records the interpretation and rejected transitive-cluster direction.
The ordinary report's arrival reducer preserves its **pre-FF-092** baseline;
use the restoration mode below for an explicit FF-092 comparison.

The report also replays an experimental substitution rule. `BestAlignment`
returns the strongest qualifying primary or sustained dHash span, including
both source offsets and tolerated gaps. The command divides that span by each
stored hash-sequence length and classifies the edge as `equivalent`,
`left_contains_right`, `right_contains_left`, or `partial_overlap`. A covered
clip requires 90% contiguous aligned coverage. A proposed replacement must
also retain at least 90% of every available technical dimension: pixel area,
frame rate, and per-frame compression budget (or spatial bitrate density when
cadence is unknown).

Those percentages are diagnostic, not production thresholds. The corpus
replay showed that longest contiguous alignment is not whole-video
substitutability: it would keep both sides on most current edges and also keeps
both sides of an accepted reviewed duplicate. The output therefore measures a
failed conservative baseline and guides the next segmented/aggregate overlap
experiment; it does not authorize a matcher or keeper change.

The second experiment aggregates all frame comparisons at an offset already
anchored by a qualifying primary or sustained window. It requires at least 75%
similarity over the complete aligned timeline and selects the widest qualifying
anchored overlap; if neither route qualifies, it retains the stronger negative
evidence. An 80% overlap covers a clip. This
recovers matches fragmented by intermittent overlays without combining
different offsets or changing the production match set. The report and review
CSV prefix these fields with `stable_`.

This stable-offset baseline also remains diagnostic. It recovers the reviewed
Mbappé containment relationship, but the independent per-frame compression
floor rejects the human-preferred 1080p50 winner. Across the full corpus it
also makes more components arrival-sensitive. The evidence separates a
coverage improvement from two unresolved policy questions: technical-quality
tradeoffs and set-level public visibility.

The cadence-aware comparison keeps pixel area, reported frame rate, and spatial
bitrate density independent. It deliberately does not divide spatial density
by frame rate: encoded frames share inter-frame information, and the reviewed
1080p50 Mbappé winner has a lower per-frame density than the inferior 720p30
cut. Historical rows without cadence cannot distinguish this experiment from
the stable-offset baseline.

The direct-cover experiment builds directional edges only from those pairwise
decisions and solves the smallest visible set for which every hidden node has a
selected direct substitute. A path through another hidden node never counts.
Components through twenty assets are exhaustive; a larger component fails
visible by retaining every asset. Equal minima prefer the sum of recorded
exact-variant observations and then asset ID, solely to make audit output
repeatable. That tiebreak is not accepted public behavior.

## Restoration experiment

`-restoration-json` compares FF-092 placement with an additive direct-support
repair after every arrival. An observed asset is restored when no selected
keeper directly matches it. A hidden intermediary and a not-yet-arrived asset
cannot justify hiding it. Restoration never retires another keeper.

The NDJSON report contains chronological prefix traces, final selected sets,
bounded arrival-order counts, recorded historical roots and missing-own-share
markers. It scopes assets by event, verification category and hash version.
Historical lineage connects analysis components but is never replacement
evidence. Singletons are omitted. `-max-permutations` accepts 1 through 100,000;
the report identifies exhaustive versus sampled components.

This is a match-only topology experiment, not a production repair plan or an
accepted whole-video replacement rule. It preserves the current quality
comparator and dHash thresholds. It does not replay credit reassignment or
FF-078 public visibility, validate original media, or recover a never-public
variant's own timestamp from its keeper. First-observation order is not recorded
asynchronous placement-completion order. The existing human labels and other
experiments are unchanged.

Use an already saved CSV with the populated module cache:

```bash
docker run --rm -i --network none --memory 4g --cpus 4 \
  -e GOMEMLIMIT=3GiB -e GOMAXPROCS=4 \
  -e GOCACHE=/gocache -e GOMODCACHE=/gomodcache \
  -v "$PWD:/src:ro" \
  -v "$HOME/.cache/found-footy/gocache:/gocache" \
  -v "$HOME/.cache/found-footy/gomodcache:/gomodcache" \
  -w /src golang:1.25.11-bookworm \
  go run -buildvcs=false ./scripts/audit_video_quality \
  -restoration-json -max-permutations 1000 \
  < scratch-audit-2026-09-08/retained-quality-corpus.csv \
  > /tmp/ff081-restoration.ndjson
```

This mode rejects combinations with `-overlap-json`, `-review-csv` and
`-pair-corpus`. See the [results](../../docs/design/audits/video-restoration-2026-09-12.md)
and [runtime proposal](../../docs/design/proposals/reversible-video-selection.md).

The domain-planner equivalence test can use the same saved export:

```bash
# Use the pinned, capped, offline test container described above, with:
# -e FF_SELECTION_CORPUS=/src/scratch-audit-2026-09-08/retained-quality-corpus.csv
go test -buildvcs=false -count=1 -run TestDomainSelection -v ./scripts/audit_video_quality
```

This test replays real hashes/technical metadata through the new domain planner
with synthetic acceptance and one source per MD5. It checks every chronological
prefix against the original experiment and reports planner-only timing. It does
not reconstruct missing source attribution, prove media availability or authorize
historical repair. PostgreSQL tests separately cover real transaction semantics.

## Popularity ownership experiment

`-popularity-json` separates exact-MD5 observations from the source counts
assigned to recorded selected clips. It validates each root's aggregate against
the saved lineage and observed counts, then tests moving one ambiguous MD5's
observations to another directly matching selected owner. Selected MD5s retain
their own exact observations. Neither quality nor the selected set changes in
this counterfactual. Scope and matcher routes remain the production ones.

Counts reconcile only when every asset has observed attribution, every lineage
terminates inside the event, every root count matches, and the roots are public
and not recorded reclaimed. Missing counts are unknown, not zero evidence.
The report withholds ranges and counterfactuals for excluded events. Ranges for
eligible events vary known-own-share ambiguous variants only; their individual
maxima are not jointly attainable. Removed/reclaimed evidence is not reassigned.

The same report runs the local pure restoration planner on each reconciled
event. Counts come from the export, but source IDs and credited-source rows are
synthetically reconstructed from aggregate lineage. Own-share media is assumed
available unless recorded reclaimed; no objects are fetched. Never-public
validation remains unknown. The resulting projection is **not a repair plan**.
Its own-share alternatives and conditional alternatives requiring missing
acceptance to confirm the inherited category have separate output fields.

Visibility follows FF-078 exactly. Rank sensitivity counts only strict reversals
between clips visible before and after; it does not fabricate missing share
creation times. One-variant moves are a bounded sensitivity check, not exhaustive
combinations or proof of whole-video substitution. No score is fed into keeper
quality, and no fractional or duplicated source credit is introduced.

For the newer saved FF-092 NDJSON export, first convert locally:

```bash
jq -rs -f scripts/audit_video_quality/history_to_csv.jq \
  scratch-audit-2026-09-11/ff092-history/assets.ndjson \
  > /tmp/ff081-popularity-corpus.csv
```

Use the capped, network-disabled Go container above with
`go run -buildvcs=false ./scripts/audit_video_quality -popularity-json`, feeding
that CSV on stdin. No fresh production export is needed. This mode rejects
other report flags. Set `FF_POPULARITY_CORPUS` to the converted file's
container-visible path to run the saved September 11 regression.
See the [results and scoring boundary](../../docs/design/audits/video-popularity-2026-09-12.md).

## Assigned versus direct support

`-direct-support-json` is a separate scoring experiment on the same saved CSV.
Direct support was subsequently adopted in the local runtime planner; see the
[decision](../../docs/decisions/2026-09-13-popularity-counts-direct-support.md).
This comparison keeps its historical assigned baseline: it derives exclusive
counts from the planner's routing, not from the planner's new direct scores.
It holds the recorded selected set fixed, then separately holds the actual
restoration planner's projected set fixed. Neither score changes keeper quality
or selection. The earlier `-popularity-json` output remains unchanged.

For each selected clip, direct support sums the observed source count of every
accepted MD5 that directly matches it in the same event/verification/hash pool.
Its own MD5 contributes once, including when its hash trace is too short for a
perceptual route. Both dHash routes matching does not count a source twice.
There is no transitive traversal, vote transfer or quality comparison. An
observed MD5 can support multiple clips; totals across clips are therefore not
counts of distinct sources. Changing exclusive ownership cannot change a fixed
clip's direct score.

The report separates known-own-acceptance support from the conditional score
if missing own validations confirm the inherited category. Removed shares are
excluded; reclaiming bytes alone does not erase retained acceptance evidence.
Playback/restoration still requires media, but counting saved source evidence
does not. Source IDs/outcomes remain absent from the saved aggregate export.
Events that fail the existing per-root attribution/public-root checks receive
no score comparison. Never-public validation is not reconstructed.

Each comparison records exact, assigned, known-direct and conditional-direct
counts; shared/unsupported source witnesses; unique covered sources versus
non-additive support totals; new/hidden public clips; strict rank reversals;
and unresolved tie changes. FF-078 and public ordering remain unchanged.
Current dHash overlap remains evidence of shared frames, not guaranteed
whole-video substitution, correct goal identity or independent corroboration.

Run the same capped offline container with `-direct-support-json` instead of
`-popularity-json`. It rejects combinations with all other report modes and
duplicate event/MD5 rows. The saved regression also uses
`FF_POPULARITY_CORPUS`. See the
[September 13 comparison](../../docs/design/audits/video-direct-support-2026-09-13.md).

## Aligned-section report

`-overlap-json` emits one NDJSON record per scoped direct pair from the saved
CSV. It skips graph permutations and does not select keepers. Combine it with
`-pair-corpus` to read either checked-in pair JSON corpus from stdin instead;
that mode also includes curated non-matches and copies existing human labels.
It rejects changed matcher/sample contracts and current-policy snapshot drift.
It never overwrites a corpus or fills missing labels. `-review-csv` and
`-overlap-json` are mutually exclusive.

Each `aligned-sections-v1` record preserves both qualified routes, their
strongest-window offsets, section coordinates, brief tolerated misses,
unassigned similar samples, and each side's unsupported prefix/suffix and
interior gaps. Supported coverage counts similar hashes in qualifying sections;
section-span coverage also includes their explicitly listed tolerated misses.
The report retains earlier policy predictions and technical metadata beside
these measurements, not as inferred human choices.

A section groups hits separated by at most two failed samples, needs a span
of ten samples, and needs 80% similarity within that span. Short/sparse groups
remain unassigned. These are diagnostic grouping settings, not content or
replacement gates. All positions are half-open sample indexes. Explicit v2
100-ms hashes have `hash_cadence_ms: 100`; legacy rows have `null` and cannot be
reported as measured seconds. Unsupported footage is not automatically an
intro/outro or disposable. One strongest offset per route cannot resolve every
replay, speed change, or insertion. See the
[results and limitations](../../docs/design/audits/video-overlap-review-2026-09-08.md).

For an offline replay using the already populated module cache:

```bash
docker run --rm -i --network=none --memory=6g --cpus=4 \
  -e GOMEMLIMIT=5GiB -e GOMAXPROCS=4 \
  -e GOCACHE=/gocache -e GOMODCACHE=/gomodcache \
  -v "$PWD:/src:ro" \
  -v "$HOME/.cache/found-footy/gocache:/gocache" \
  -v "$HOME/.cache/found-footy/gomodcache:/gomodcache" \
  -w /src golang:1.25.11-bookworm \
  go run -buildvcs=false ./scripts/audit_video_quality \
  -overlap-json -pair-corpus \
  < scripts/audit_video_quality/testdata/cadence-pairs-2026-09-08.json \
  > /tmp/ff081-overlap-natural.ndjson
```

Use `testdata/reviewed-pairs.json` for the August judgments. For a saved SQL
CSV, remove `-pair-corpus` and redirect that local file to stdin. No new
production export is needed for the preserved September data.

## Human review manifest

Pass `-review-csv` to emit one stable row per direct perceptual match instead
of the text report. It includes both route windows, the selected route, aligned
offsets and gaps, bilateral coverage, coverage class, and experimental action.
The command fills reproducible evidence through `current_preference` and
leaves four reviewer-owned columns blank:

- `dedup_decision`: `collapse`, `keep_both`, or `uncertain`.
- `quality_winner`: `left`, `right`, `tie`, `not_applicable`, or `uncertain`.
- `quality_reasons`: semicolon-separated visible reasons such as `cadence`,
  `compression`, `resolution`, `completeness`, `crop`, `screen_recording`,
  `overlay`, or `presentation`.
- `notes`: short evidence that the bounded labels do not express.

Frame rate, spatial bitrate density, and bits per pixel per frame are separate
evidence columns. Never infer the quality winner from one of them alone. A
60 fps clip may spend fewer bits on each frame than a 30 fps clip and still be
the better presentation if it retains more distinct motion frames. Encoded FPS
alone does not establish that advantage.

The source tweet URL is selected from immutable `observed_asset_id` when the
FF-083 attribution exists, with old outcome detail as a historical fallback. A
superseded share resolves to its current winner, and a never-public variant has
no share, so reviewers use the source URL to inspect retained evidence when the
tweet still exists.

## Reviewed regression corpus

[`testdata/reviewed-pairs.json`](./testdata/reviewed-pairs.json) preserves ten
accepted pair judgments from the 2026-08-31 review. It contains only derived
dHash sequences, retained technical metadata, human labels, and a snapshot of
the current matcher and comparator results. It contains no video, image, tweet
text, or media URL.

The two outcomes are deliberately separate:

- `human` records whether the presentations should collapse and which one a
  reviewer would retain.
- `current` records what the production matcher and comparator do today.

Tests replay `current` from the stored evidence and validate `human` as an
independent product judgment. A known mismatch, such as J. King's visibly
cleaner short cut losing to the duration-first comparator, is regression
evidence rather than a failing assertion. This lets a future policy measure
which accepted cases it improves without silently rewriting the labels.

## Natural cadence evidence, partially reviewed

[`testdata/cadence-pairs-2026-09-08.json`](./testdata/cadence-pairs-2026-09-08.json)
preserves the five post-FF-082/083 first-loss pairs inspected in the
[September cadence review](../../docs/design/audits/video-cadence-review-2026-09-08.md).
It retains derived hashes, metadata, exact observations, source-copy checksums,
frame diagnostics, and current/experimental outcome snapshots. It includes no
media or URLs. Adams now has an explicit `human` label: collapse the pair and
retain the shorter right copy. The other four pairs remain unlabelled.

Tests replay the observed behavior and coverage experiments, not desired
winners. Source-frame inspection and decimation diagnostics do not prove
native motion FPS or substitute for user review. The separate
[cadence experiment](../audit_video_cadence/README.md) measures repeated-frame
patterns; its output never supplies a human label or changes keeper policy.
